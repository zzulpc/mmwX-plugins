package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	fetchMaxBytes = 16 << 20
	fetchTimeout  = 45 * time.Second
	// 小批量订阅可等待空位；两个下载共享进程额度，避免每个任务各自放大内存占用。
	fetchConcurrency     = 2
	fetchMaxInflightJobs = 4
)

var (
	fetchJobSlots           = make(chan struct{}, fetchMaxInflightJobs)
	fetchSlots              = make(chan struct{}, fetchConcurrency)
	errFetchTargetNotPublic = errors.New("订阅抓取不允许访问内网或保留地址")
)

func dispatchFetchJob(parentCtx context.Context, job wsMsg, send func(wsMsg) error) {
	dispatchFetchJobWithClient(parentCtx, job, send, newFetchClient(), fetchTimeout)
}

// 连接取消同时结束下载与排队；独立客户端参数让正常抓取也能用本地服务器验收。
func dispatchFetchJobWithClient(parentCtx context.Context, job wsMsg, send func(wsMsg) error, client *http.Client, budget time.Duration) {
	ctx, cancel := context.WithTimeout(parentCtx, budget)
	if ctx.Err() != nil {
		cancel()
		return
	}
	select {
	case fetchJobSlots <- struct{}{}:
		go func() {
			defer cancel()
			defer client.CloseIdleConnections()
			defer func() { <-fetchJobSlots }()
			var result wsMsg
			select {
			case fetchSlots <- struct{}{}:
				result = fetchSubscription(ctx, job, client)
				// 回包还持有 body，直到发送结束再释放下载槽，避免积压多份大响应。
				defer func() { <-fetchSlots }()
			case <-ctx.Done():
				result = fetchFailure(job, "等待抓取超时")
			}
			if parentCtx.Err() != nil {
				return // 旧连接的结果没有接收方，不向重连后的主控补发。
			}
			if err := send(result); err != nil {
				log.Printf("[speedtester] 回传订阅抓取结果失败")
			}
		}()
	default:
		cancel()
		client.CloseIdleConnections()
		_ = send(fetchFailure(job, "订阅抓取任务较多，请稍后重试"))
	}
}

func fetchFailure(job wsMsg, message string) wsMsg {
	return wsMsg{Type: "fetch_result", JobID: job.JobID, Status: "failed", Error: message}
}

func fetchSubscription(ctx context.Context, job wsMsg, client *http.Client) wsMsg {
	target, err := url.Parse(strings.TrimSpace(job.URL))
	if err != nil || target == nil || target.Hostname() == "" {
		return fetchFailure(job, "订阅 URL 无效")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return fetchFailure(job, "订阅抓取只支持 http/https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fetchFailure(job, "无法构造订阅请求")
	}
	userAgent := strings.TrimSpace(job.UserAgent)
	if userAgent == "" {
		userAgent = "clash-meta/2.4.0"
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return fetchFailure(job, fetchErrorMessage(err))
	}
	defer resp.Body.Close()
	// 不能只看 Content-Length，分块响应与解压后的内容同样必须受上限约束。
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes+1))
	if err != nil {
		return fetchFailure(job, fetchErrorMessage(err))
	}
	if len(body) > fetchMaxBytes {
		return fetchFailure(job, fmt.Sprintf("订阅响应超过 %d MiB 上限", fetchMaxBytes>>20))
	}
	if err := ctx.Err(); err != nil {
		return fetchFailure(job, fetchErrorMessage(err))
	}
	headers := make(map[string]string)
	for _, name := range []string{"subscription-userinfo", "content-disposition", "profile-update-interval", "content-type"} {
		if value := resp.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	// HTTP 错误页也保留状态码与响应体，由主控解释订阅服务的错误，不擅自判定内容格式。
	return wsMsg{Type: "fetch_result", JobID: job.JobID, Status: "ok", StatusCode: resp.StatusCode,
		Headers: headers, Body: base64.StdEncoding.EncodeToString(body)}
}

func fetchErrorMessage(err error) string {
	// net/http 的错误常包含带令牌的完整 URL，包括重定向地址；不直接拼进日志或回包。
	switch {
	case errors.Is(err, errFetchTargetNotPublic):
		return errFetchTargetNotPublic.Error()
	case errors.Is(err, context.Canceled):
		return "订阅抓取已取消"
	case errors.Is(err, context.DeadlineExceeded):
		return "订阅抓取超时"
	default:
		return "订阅抓取失败，请检查地址、网络或证书"
	}
}

func newFetchClient() *http.Client {
	// 订阅中转需要测速端自己的出口，不能借用代理内核或环境中的 HTTP 代理。
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: controlFetchTarget}
	return &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

func controlFetchTarget(_ string, address string, _ syscall.RawConn) error {
	// Control 收到实际准备连接的 IP，每次重定向同样经过这里；不先解析再用域名二次拨号。
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil {
		return errFetchTargetNotPublic
	}
	ip := net.IP(endpoint.Addr().AsSlice())
	if !probeTargetAllowed(ip) {
		return errFetchTargetNotPublic
	}
	if v4 := ip.To4(); v4 != nil && (v4[0] == 0 || v4[0] == 192 && v4[1] == 0 && v4[2] == 0) {
		return errFetchTargetNotPublic
	}
	return nil
}
