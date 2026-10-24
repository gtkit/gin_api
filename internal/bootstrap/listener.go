package bootstrap

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/gtkit/logger/v2"
)

// certExpiryWarnWindow 是证书临期告警窗口。
//
// 只告警不拒绝：一张还有 3 天到期的证书**现在仍然有效**，拒绝启动等于
// 用一次确定的停机换一个尚未发生的问题。但它必须被说出来——
// 证书过期几乎总是"没人注意到"造成的。
const certExpiryWarnWindow = 14 * 24 * time.Hour

// prebindResult 持有预绑定的监听器与已校验的 TLS 材料。
type prebindResult struct {
	listener net.Listener
	// certificate 仅在启用 TLS 时非 nil。
	certificate *tls.Certificate
}

// prebindHTTP 在**开始服务之前**完成两件必须前置的事：
//
//  1. 绑定监听端口；
//  2. 启用 TLS 时加载并校验密钥对与证书有效期。
//
// 为什么必须前置：这两件事都是"要么立刻成功、要么永远不会成功"的。
// 放到 Serve 里做，失败发生在 worker 启动屏障**之后**——
// 而 required one-shot 可能已经执行了不可逆的副作用（数据修复、发通知）。
// 一个端口占用或一张过期证书，代价就变成了"副作用已发生但服务起不来"。
//
// 失败时不留下任何已打开的资源：监听器在返回错误前关闭。
func prebindHTTP(cfg HTTPConfig) (*prebindResult, error) {
	var certificate *tls.Certificate
	if cfg.EnableTLS {
		// 先验证书再绑端口：证书问题比端口占用更常见，
		// 先验它能让失败路径不必清理监听器。
		cert, err := loadAndVerifyCertificate(cfg)
		if err != nil {
			return nil, err
		}
		certificate = cert
		warnCertificateExpiry(cert)
	}

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", addr, err)
	}

	return &prebindResult{listener: listener, certificate: certificate}, nil
}

// Close 释放预绑定的监听器。幂等由 net.Listener 自身保证之外，
// 这里容忍 nil 以简化失败路径。
func (p *prebindResult) Close() error {
	if p == nil || p.listener == nil {
		return nil
	}
	return p.listener.Close()
}

// loadAndVerifyCertificate 加载密钥对并校验有效期。
//
// 三类问题必须在启动期区分清楚，因为它们的处置完全不同：
//
//	密钥对不匹配 / 文件损坏 → 启动失败（服务起来也建不成任何 TLS 连接）
//	证书尚未生效 / 已过期   → 启动失败（每个客户端都会握手失败）
//	证书临近过期           → 告警，正常启动
func loadAndVerifyCertificate(cfg HTTPConfig) (*tls.Certificate, error) {
	var cert tls.Certificate
	if cfg.TLSConfig != nil && len(cfg.TLSConfig.Certificates) > 0 {
		cert = cfg.TLSConfig.Certificates[0]
	} else {
		var err error
		cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load tls key pair (cert=%s key=%s): %w", cfg.CertFile, cfg.KeyFile, err)
		}
	}

	if len(cert.Certificate) == 0 {
		return nil, errors.New("tls key pair contains no certificate")
	}

	leaf := cert.Leaf
	if leaf == nil {
		var err error
		leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("parse tls leaf certificate: %w", err)
		}
	}

	now := time.Now()
	switch {
	case now.Before(leaf.NotBefore):
		return nil, fmt.Errorf(
			"tls certificate is not valid until %s; every handshake would fail", leaf.NotBefore.UTC())
	case now.After(leaf.NotAfter):
		return nil, fmt.Errorf(
			"tls certificate expired at %s; every handshake would fail", leaf.NotAfter.UTC())
	}

	cert.Leaf = leaf
	return &cert, nil
}

// Warn at prebind, after logging is installed, rather than during local validation.
func warnCertificateExpiry(cert *tls.Certificate) {
	if remaining := time.Until(cert.Leaf.NotAfter); remaining < certExpiryWarnWindow {
		logger.Warnf("tls certificate expires at %s (in %s); renew it before then",
			cert.Leaf.NotAfter.UTC(), remaining.Truncate(time.Hour))
	}
}
