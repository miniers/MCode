package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mcode/internal/config"
	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
	"mcode/internal/webassets"
)

const remoteTLSDirName = "remote-tls"

// loadRemoteConfig 读取远程控制配置；文件缺失时使用默认配置。
func (s *RemoteService) loadRemoteConfig() config.RemoteConfig {
	cfg, err := config.Load(s.dataDir)
	if err != nil {
		cfg = config.Defaults()
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg.Remote
}

// startTLSLocked 在已持有 s.mu 时创建并启动 TLS 监听与服务。
func (s *RemoteService) startTLSLocked(cfg config.RemoteConfig, lanHosts []string, dispatcher *remote.Dispatcher) {
	// 推送的 VAPID sub 与 TLS 回调都要知道 TLS 入口的域名与允许的 Host，先记下再启停监听。
	s.tlsHosts = append([]string(nil), lanHosts...)
	if cfg.TLSDomain != "" {
		s.tlsHosts = append(s.tlsHosts, cfg.TLSDomain)
	}

	if s.tlsLn != nil {
		_ = s.tlsLn.Close()
	}
	if s.tlsMgr != nil {
		s.tlsMgr.Close()
	}
	s.tlsLn = nil
	s.tlsSrv = nil
	s.tlsMgr = nil
	s.tlsDone = nil

	s.tlsErr = ""
	s.tlsStartedURL = ""
	s.tlsBase.Store("")

	if cfg.TLSListen == "" {
		return
	}

	ln, err := net.Listen("tcp", cfg.TLSListen)
	if err != nil {
		s.tlsErr = err.Error()
		return
	}

	cfToken, _ := config.NewSecretStore("mcode").Get("cloudflare_api_token")

	tlsHosts := append([]string(nil), s.tlsHosts...)

	mgr, err := remote.NewTLSManager(remote.TLSOptions{
		Dir:             filepath.Join(s.dataDir, remoteTLSDirName),
		Hosts:           tlsHosts,
		Domain:          cfg.TLSDomain,
		Email:           cfg.ACMEEmail,
		DirectoryURL:    cfg.ACMEDirectory,
		CloudflareToken: cfToken,
	})
	if err != nil {
		_ = ln.Close()
		s.tlsErr = err.Error()
		return
	}
	if err := mgr.Start(context.Background()); err != nil {
		mgr.Close()
		_ = ln.Close()
		s.tlsErr = err.Error()
		return
	}

	srv := remote.NewServer(remote.ServerOptions{
		Handler:       dispatcher,
		Assets:        webassets.FS(),
		ListenAddr:    ln.Addr().String(),
		Entry:         remote.EntryTLS,
		AllowedHosts:  tlsHosts,
		AdminRoutes:   s.adminRoutesLocked(),
		AnswerHandler: s.answerHandlerLocked(remote.EntryTLS),
	})

	if cfg.TLSDomain != "" {
		s.tlsStartedURL = tlsDomainBaseURL(cfg.TLSDomain, ln.Addr())
	} else {
		s.tlsStartedURL = "https://" + ln.Addr().String()
	}
	s.tlsBase.Store(s.tlsStartedURL)

	done := make(chan struct{})
	s.tlsMgr = mgr
	s.tlsSrv = srv
	s.tlsLn = ln
	s.tlsDone = done

	go func() {
		defer close(done)
		if err := srv.ServeTLS(ln, mgr.TLSConfig()); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			if s.tlsSrv == srv {
				s.tlsErr = err.Error()
			}
			s.mu.Unlock()
		}
	}()
}

// tlsDomainBaseURL 把配置的域名与实际 TLS 监听端口拼成对外地址；端口为 443 时省略。
func tlsDomainBaseURL(domain string, addr net.Addr) string {
	if addr == nil {
		return "https://" + domain
	}
	_, port, _, _ := parseHostPort(addr)
	if port <= 0 || port == 443 {
		return "https://" + domain
	}
	return "https://" + net.JoinHostPort(domain, strconv.Itoa(port))
}

// TLSStatus 获取当前 TLS 证书与监听状态。
func (s *RemoteService) TLSStatus() mcodeui.RemoteTLSStatus {
	s.mu.Lock()
	tlsLn := s.tlsLn
	startedURL := s.tlsStartedURL
	errMsg := s.tlsErr
	mgr := s.tlsMgr
	dataDir := s.dataDir
	s.mu.Unlock()

	cfg := s.loadRemoteConfig()
	st := mcodeui.RemoteTLSStatus{
		Configured:         cfg.TLSListen != "",
		Domain:             cfg.TLSDomain,
		StartedURL:         startedURL,
		Error:              errMsg,
		HasCloudflareToken: hasRemoteCloudflareToken(),
	}

	if tlsLn != nil {
		_, port, _, _ := parseHostPort(tlsLn.Addr())
		st.TLSPort = port
	} else if cfg.TLSListen != "" {
		if _, portStr, err := net.SplitHostPort(cfg.TLSListen); err == nil {
			st.TLSPort, _ = strconv.Atoi(portStr)
		}
	}

	if mgr == nil && dataDir != "" {
		mgr, _ = remote.NewTLSManager(remote.TLSOptions{Dir: filepath.Join(dataDir, remoteTLSDirName)})
	}
	if mgr != nil {
		if info, err := mgr.CertificateInfo(); err == nil {
			st.CertDomains = info.Domains
			st.Issuer = info.Issuer
			st.NotAfter = info.NotAfter
			st.Source = info.Source
		}
	}

	return st
}

// IssueTLSCertificate 使用 Cloudflare DNS-01 立即签发证书并热加载。
func (s *RemoteService) IssueTLSCertificate(ctx context.Context) (mcodeui.RemoteTLSStatus, error) {
	cfg := s.loadRemoteConfig()
	if strings.TrimSpace(cfg.TLSDomain) == "" {
		return s.TLSStatus(), errors.New("请先填写域名")
	}

	token, _ := config.NewSecretStore("mcode").Get("cloudflare_api_token")
	if strings.TrimSpace(token) == "" {
		return s.TLSStatus(), errors.New("请先保存 Cloudflare API Token")
	}

	hosts := []string{"localhost", "127.0.0.1"}
	hosts = append(hosts, remote.LANIPv4s()...)
	hosts = append(hosts, cfg.ExtraHosts...)

	// issueErr 保存「证书已落盘但解析未同步」这类非致命错误，热加载后再返回给界面。
	var issueErr error
	mgr, err := remote.NewTLSManager(remote.TLSOptions{
		Dir:             filepath.Join(s.dataDir, remoteTLSDirName),
		Hosts:           hosts,
		Domain:          strings.TrimSpace(cfg.TLSDomain),
		Email:           cfg.ACMEEmail,
		DirectoryURL:    cfg.ACMEDirectory,
		CloudflareToken: token,
	})
	if err != nil {
		return s.TLSStatus(), err
	}
	if err := mgr.IssueCertificate(ctx); err != nil {
		// 证书已落盘但 A 记录未同步时，仍继续热加载新证书；错误在加载后返回给界面提示。
		if !errors.Is(err, remote.ErrDNSSyncFailed) {
			return s.TLSStatus(), err
		}
		issueErr = err
	}

	s.mu.Lock()
	runningMgr := s.tlsMgr
	s.mu.Unlock()
	if runningMgr != nil {
		if err := runningMgr.Reload(); err != nil {
			return s.TLSStatus(), err
		}
	}

	if issueErr != nil {
		return s.TLSStatus(), issueErr
	}

	return s.TLSStatus(), nil
}

// ImportTLSCertificate 导入外部证书链与私钥并热加载。
func (s *RemoteService) ImportTLSCertificate(ctx context.Context, chainPath, keyPath string) (mcodeui.RemoteTLSStatus, error) {
	s.mu.Lock()
	mgr := s.tlsMgr
	s.mu.Unlock()

	if mgr == nil {
		var err error
		mgr, err = remote.NewTLSManager(remote.TLSOptions{Dir: filepath.Join(s.dataDir, remoteTLSDirName)})
		if err != nil {
			return s.TLSStatus(), err
		}
	}

	if err := mgr.ImportCertificate(chainPath, keyPath); err != nil {
		return s.TLSStatus(), err
	}
	return s.TLSStatus(), nil
}

// RemoveTLSCertificate 停止 TLS 监听并删除已配置的证书。
func (s *RemoteService) RemoveTLSCertificate(ctx context.Context) error {
	s.mu.Lock()
	mgr := s.tlsMgr
	srv := s.tlsSrv
	ln, done := s.tlsLn, s.tlsDone
	s.tlsMgr = nil
	s.tlsSrv = nil
	s.tlsLn = nil
	s.tlsDone = nil
	s.tlsStartedURL = ""
	s.tlsErr = ""
	s.tlsBase.Store("")
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	if done != nil {
		<-done
	}
	if srv != nil {
		_ = srv.Shutdown(ctx)
	}

	if mgr == nil {
		var err error
		mgr, err = remote.NewTLSManager(remote.TLSOptions{Dir: filepath.Join(s.dataDir, remoteTLSDirName)})
		if err != nil {
			return err
		}
	}
	err := mgr.RemoveCertificate()
	mgr.Close()
	return err
}

func hasRemoteCloudflareToken() bool {
	token, err := config.NewSecretStore("mcode").Get("cloudflare_api_token")
	return err == nil && strings.TrimSpace(token) != ""
}
