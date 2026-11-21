package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"
)

func main() {
	if e := entry(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func entry() error {
	mode := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		mode = args[0]
		args = args[1:]
	}
	if mode == "version" {
		fmt.Println(version)
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	sub := ""
	if mode == "agent" {
		if len(args) > 0 {
			sub = args[0]
			args = args[1:]
		} else {
			return errors.New("用法：system-monitor agent enroll|run")
		}
	}
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	defaultDir := "data"
	if mode == "agent" {
		defaultDir = "agent-data"
	}
	root := flags.String("data", defaultDir, "initial internal data directory")
	hub := flags.String("hub", "", "enrollment hub URL")
	id := flags.String("id", "", "enrollment server ID")
	code := flags.String("code", "", "one-time enrollment code")
	insecure := flags.Bool("insecure", false, "initial enrollment: accept self-signed hub TLS")
	proxy := flags.String("proxy", "", "initial enrollment proxy")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if mode != "serve" && mode != "agent" && mode != "recover-access" {
		return errors.New("用法：system-monitor serve | agent enroll | agent run | recover-access | version")
	}
	if mode == "agent" && runtime.GOOS != "linux" {
		return errors.New("探针仅支持 Linux；监控面板支持 Linux 和 Windows")
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		return e
	}
	release, e := lockDirectory(abs)
	if e != nil {
		return e
	}
	defer release()
	s, e := openStore(abs)
	if e != nil {
		return e
	}
	if mode == "agent" {
		defer s.Close()
		if sub == "enroll" {
			bootstrap := defaultServer()
			bootstrap.VerifyTLS = !*insecure
			bootstrap.Proxy = *proxy
			if !validHTTP(*proxy, true) {
				return errors.New("初始代理地址无效")
			}
			if e = enrollAgent(ctx, s, *hub, *id, *code, bootstrap); e != nil {
				return e
			}
			fmt.Println("探针接入成功")
			return nil
		}
		if sub != "run" {
			return errors.New("未知探针命令")
		}
		return runAgent(ctx, s)
	}
	for i := 0; s.meta("relocatedTo") != ""; i++ {
		if i >= 8 {
			s.Close()
			return errors.New("数据目录重定位链过长")
		}
		next := s.meta("relocatedTo")
		s.Close()
		unlock, e := lockDirectory(next)
		if e != nil {
			return e
		}
		defer unlock()
		s, e = openStore(next)
		if e != nil {
			return e
		}
	}
	if mode == "recover-access" {
		defer s.Close()
		name, password, e := s.RecoverAccess()
		if e != nil {
			return e
		}
		fmt.Println("本机访问恢复完成；IP 限制已清除、旧会话已失效。")
		fmt.Println("账号：", name)
		fmt.Println("临时密码：", password)
		return nil
	}
	if s.NeedsSetup() {
		fmt.Println("首次初始化码（仅在本机显示）：", s.SetupCode())
	}
	return serveHub(ctx, s)
}
func serviceTLS(s *Store, c ServiceConfig) (*tls.Config, error) {
	if c.TLSMode != "builtin" {
		return nil, nil
	}
	cert, e := tls.X509KeyPair([]byte(s.Secret("tlsCertificate")), []byte(s.Secret("tlsPrivateKey")))
	if e != nil {
		return nil, errors.New("内置 HTTPS 证书不可用")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}
func listenService(c ServiceConfig) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(c.ListenAddress, fmt.Sprint(c.ListenPort)))
}
func serveHub(ctx context.Context, s *Store) error {
	defer func() { s.Close() }()
	active := defaultConfig(s.Root).Service
	_ = json.Unmarshal([]byte(s.meta("activeService")), &active)
	apply := make(chan struct{}, 1)
	listener, e := listenService(active)
	if e != nil {
		return e
	}
	var releases []func()
	defer func() {
		for _, f := range releases {
			f()
		}
	}()
	for {
		tlsConfig, e := serviceTLS(s, active)
		if e != nil {
			listener.Close()
			return e
		}
		rawListener := listener
		if tlsConfig != nil {
			listener = tls.NewListener(rawListener, tlsConfig)
		}
		server := &http.Server{Handler: newHubAPI(s, apply), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Duration(active.RequestTimeout) * time.Second, WriteTimeout: time.Duration(active.RequestTimeout) * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		workers, stopWorkers := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for _, fn := range []func(context.Context, *Store){monitorLoop, notificationLoop, httpProbeLoop} {
			wg.Add(1)
			go func(f func(context.Context, *Store)) { defer wg.Done(); f(workers, s) }(fn)
		}
		writer := &runtimeLog{s: s}
		log.SetOutput(io.MultiWriter(os.Stderr, writer))
		log.Printf("system-monitor %s listening on %s", version, rawListener.Addr())
		scheme := "http"
		if tlsConfig != nil {
			scheme = "https"
		}
		fmt.Printf("访问：%s://127.0.0.1:%d\n", scheme, active.ListenPort)
		ended := make(chan error, 1)
		go func() { ended <- server.Serve(listener) }()
		applyRequested := false
		select {
		case <-ctx.Done():
		case <-apply:
			applyRequested = true
		case err := <-ended:
			if !errors.Is(err, http.ErrServerClosed) {
				stopWorkers()
				wg.Wait()
				return err
			}
		}
		stopWorkers()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = server.Shutdown(shutdown)
		cancel()
		_ = server.Close()
		wg.Wait()
		if !applyRequested || ctx.Err() != nil {
			return nil
		}
		desired, _ := s.Config()
		previous := s
		previousActive := active
		var newStore *Store
		var newRelease func()
		var newListener net.Listener
		e = desired.Validate()
		if e == nil {
			_, e = serviceTLS(s, desired.Service)
		}
		if e == nil {
			newListener, e = listenService(desired.Service)
		}
		if e == nil && !samePath(s.Root, desired.Storage.DataDirectory) {
			newStore, newRelease, e = migrateStore(s, desired.Storage.DataDirectory)
		}
		if e == nil {
			if newStore != nil {
				if e = previous.setMeta("relocatedTo", newStore.Root); e == nil {
					s = newStore
					releases = append(releases, newRelease)
					_ = previous.Close()
				} else {
					newStore.Close()
					newRelease()
				}
			}
		}
		if e != nil {
			if newListener != nil {
				newListener.Close()
			}
			s = previous
			active = previousActive
			_ = s.setMeta("applyStatus", "应用失败，已恢复原服务；请检查端口、证书及目标目录："+e.Error())
			listener, e = listenService(active)
			if e != nil {
				return e
			}
			continue
		}
		active = desired.Service
		listener = newListener
		_ = s.setMeta("activeService", encode(active))
		_ = s.setMeta("applyStatus", "应用成功 · "+time.Now().Format(time.RFC3339))
		s.Event("", "runtime", "success", "服务设置已应用", net.JoinHostPort(active.ListenAddress, fmt.Sprint(active.ListenPort)), "")
	}
}
func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
func migrateStore(s *Store, target string) (*Store, func(), error) {
	target = filepath.Clean(target)
	if _, e := os.Stat(target); !errors.Is(e, os.ErrNotExist) {
		return nil, nil, errors.New("迁移目标必须是尚不存在的绝对目录")
	}
	rel, e := filepath.Rel(s.Root, target)
	if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, nil, errors.New("迁移目标不能位于当前数据目录内部")
	}
	if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return nil, nil, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(target), ".monitor-migrate-")
	if e != nil {
		return nil, nil, e
	}
	defer os.RemoveAll(stage)
	if e = s.SnapshotTo(filepath.Join(stage, "monitor.db")); e != nil {
		return nil, nil, e
	}
	if e = copyExclusive(filepath.Join(s.Root, "identity.key"), filepath.Join(stage, "identity.key"), 0600); e != nil {
		return nil, nil, e
	}
	for _, folder := range []string{"backups", "logs"} {
		source := filepath.Join(s.Root, folder)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		}
		e = filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("数据归档不能包含符号链接")
			}
			rel, err := filepath.Rel(s.Root, path)
			if err != nil {
				return err
			}
			dest := filepath.Join(stage, rel)
			if entry.IsDir() {
				return os.MkdirAll(dest, 0700)
			}
			if !entry.Type().IsRegular() {
				return errors.New("数据归档包含非普通文件")
			}
			return copyStream(path, dest)
		})
		if e != nil {
			return nil, nil, e
		}
	}
	if e = validateBundle(stage); e != nil {
		return nil, nil, e
	}
	if e = os.Rename(stage, target); e != nil {
		return nil, nil, e
	}
	unlock, e := lockDirectory(target)
	if e != nil {
		return nil, nil, e
	}
	next, e := openStore(target)
	if e != nil {
		unlock()
		return nil, nil, e
	}
	_ = next.setMeta("relocatedTo", "")
	return next, unlock, nil
}
func copyStream(source, dest string) error {
	in, e := os.Open(source)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	if e == nil {
		e = out.Sync()
	}
	closeErr := out.Close()
	if e == nil {
		e = closeErr
	}
	return e
}

type runtimeLog struct {
	s  *Store
	mu sync.Mutex
}

func (w *runtimeLog) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, _ := w.s.Config()
	dir := filepath.Join(w.s.Root, "logs")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return 0, e
	}
	path := filepath.Join(dir, "runtime.log")
	if info, e := os.Stat(path); e == nil && info.Size()+int64(len(b)) > int64(c.Storage.LogFileCap)*(1<<20) {
		_ = os.Rename(path, filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000000000")+".log"))
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return 0, e
	}
	n, e := f.Write(b)
	_ = f.Close()
	entries, _ := os.ReadDir(dir)
	var size int64
	for i := len(entries) - 1; i >= 0; i-- {
		v := entries[i]
		info, err := v.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		size += info.Size()
		if v.Name() != "runtime.log" && (time.Since(info.ModTime()) > time.Duration(c.Storage.LogDays)*24*time.Hour || size > int64(c.Storage.LogCap)*(1<<20)) {
			_ = os.Remove(filepath.Join(dir, v.Name()))
		}
	}
	return n, e
}
