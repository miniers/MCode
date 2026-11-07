package memspace

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/nmem"
)

const spacesJSON = `{"enabled":true,"spaces":[
  {"id":"default","key":"default","name":"Default","aliases":[]},
  {"id":"mcode","key":"mcode","name":"mcode","aliases":["MyCode"]}]}`

func stub(t *testing.T, gitOut string, gitErr error, spaces string) *int {
	t.Helper()
	calls := 0
	runCmd = func(dir, name string, args ...string) ([]byte, error) {
		calls++
		if name == "git" {
			return []byte(gitOut), gitErr
		}
		return []byte(spaces), nil
	}
	cache = map[string]cacheEntry{}
	restCache = map[string]cacheEntry{}
	t.Setenv(envKey, "")
	t.Cleanup(func() {
		runCmd = defaultRunCmd
		now = time.Now
		cache = map[string]cacheEntry{}
		restCache = map[string]cacheEntry{}
	})
	return &calls
}

func TestEnvMatchesExistingSpace(t *testing.T) {
	stub(t, "/Users/x/code/MCode/.git\n", nil, spacesJSON)
	got := Env("/work")
	if want := []string{"NMEM_SPACE=mcode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Env = %v, want %v", got, want)
	}
}

func TestEnvMatchesAlias(t *testing.T) {
	stub(t, "/Users/x/code/mycode/.git\n", nil, spacesJSON)
	if got := Env("/work"); len(got) != 1 || got[0] != "NMEM_SPACE=mycode" {
		t.Fatalf("Env = %v", got)
	}
}

func TestEnvNilCases(t *testing.T) {
	t.Run("no matching space", func(t *testing.T) {
		stub(t, "/Users/x/code/other/.git\n", nil, spacesJSON)
		if got := Env("/work"); got != nil {
			t.Fatalf("Env = %v, want nil", got)
		}
	})
	t.Run("not a git repo", func(t *testing.T) {
		stub(t, "", errors.New("not a git repo"), spacesJSON)
		if got := Env("/work"); got != nil {
			t.Fatalf("Env = %v, want nil", got)
		}
	})
	t.Run("default is never injected", func(t *testing.T) {
		stub(t, "/Users/x/Default/.git\n", nil, spacesJSON)
		if got := Env("/work"); got != nil {
			t.Fatalf("Env = %v, want nil", got)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		stub(t, "/Users/x/mcode/.git\n", nil, "not json")
		if got := Env("/work"); got != nil {
			t.Fatalf("Env = %v, want nil", got)
		}
	})
	t.Run("explicit env wins", func(t *testing.T) {
		calls := stub(t, "/Users/x/mcode/.git\n", nil, spacesJSON)
		t.Setenv(envKey, "custom")
		if got := Env("/work"); got != nil || *calls != 0 {
			t.Fatalf("Env = %v calls=%d, want nil and no commands", got, *calls)
		}
	})
	t.Run("empty workDir", func(t *testing.T) {
		stub(t, "/Users/x/mcode/.git\n", nil, spacesJSON)
		if got := Env(""); got != nil {
			t.Fatalf("Env = %v, want nil", got)
		}
	})
}

func TestEnvCachesWithinTTL(t *testing.T) {
	calls := stub(t, "/Users/x/mcode/.git\n", nil, spacesJSON)
	base := time.Now()
	now = func() time.Time { return base }
	Env("/work")
	Env("/work")
	if *calls != 2 {
		t.Fatalf("commands run = %d, want 2 (git + nmem, once)", *calls)
	}
	now = func() time.Time { return base.Add(cacheTTL + time.Second) }
	Env("/work")
	if *calls != 4 {
		t.Fatalf("commands run after TTL = %d, want 4", *calls)
	}
}

// spacesServer 启动一个模拟 GET /spaces 的服务，返回 spacesJSON，并统计请求次数。
func spacesServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/spaces" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(spacesJSON))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestResolveMatchesExistingSpace(t *testing.T) {
	srv, hits := spacesServer(t)
	stub(t, "/Users/x/code/MCode/.git\n", nil, spacesJSON)
	client := nmem.New(srv.URL, "nmem_test")

	if got := Resolve(context.Background(), client, "/work", ""); got != "mcode" {
		t.Fatalf("Resolve = %q, want mcode", got)
	}
	// 1 分钟缓存：同一 workDir 再调用不再发请求。
	Resolve(context.Background(), client, "/work", "")
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("/spaces 请求次数 = %d, want 1", got)
	}
}

func TestResolveByAliasAndOverride(t *testing.T) {
	srv, hits := spacesServer(t)
	stub(t, "/Users/x/code/mycode/.git\n", nil, spacesJSON)
	client := nmem.New(srv.URL, "nmem_test")

	if got := Resolve(context.Background(), client, "/work", ""); got != "mcode" {
		t.Fatalf("Resolve by alias = %q, want mcode", got)
	}

	// override 非空直接返回，不发请求也不读缓存。
	if got := Resolve(context.Background(), client, "/work", " team "); got != "team" {
		t.Fatalf("Resolve override = %q, want team", got)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("/spaces 请求次数 = %d, want 1", got)
	}
}

func TestResolveNilCases(t *testing.T) {
	srv, hits := spacesServer(t)
	client := nmem.New(srv.URL, "nmem_test")

	t.Run("nil client", func(t *testing.T) {
		stub(t, "/Users/x/code/mcode/.git\n", nil, spacesJSON)
		if got := Resolve(context.Background(), nil, "/work", ""); got != "" {
			t.Fatalf("Resolve = %q, want empty", got)
		}
	})
	t.Run("empty workDir", func(t *testing.T) {
		stub(t, "/Users/x/code/mcode/.git\n", nil, spacesJSON)
		if got := Resolve(context.Background(), client, "", ""); got != "" {
			t.Fatalf("Resolve = %q, want empty", got)
		}
	})
	t.Run("not a git repo", func(t *testing.T) {
		stub(t, "", errors.New("not a git repo"), spacesJSON)
		if got := Resolve(context.Background(), client, "/no-git", ""); got != "" {
			t.Fatalf("Resolve = %q, want empty", got)
		}
	})
	t.Run("no matching space", func(t *testing.T) {
		stub(t, "/Users/x/code/other/.git\n", nil, spacesJSON)
		if got := Resolve(context.Background(), client, "/other", ""); got != "" {
			t.Fatalf("Resolve = %q, want empty", got)
		}
	})
	t.Run("request failure is not cached", func(t *testing.T) {
		stub(t, "/Users/x/code/mcode/.git\n", nil, spacesJSON)
		down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer down.Close()
		broken := nmem.New(down.URL, "nmem_test")
		if got := Resolve(context.Background(), broken, "/broken", ""); got != "" {
			t.Fatalf("Resolve = %q, want empty", got)
		}
		if e, ok := restCache["/broken"]; ok {
			t.Fatalf("失败结果不应写入缓存: %+v", e)
		}
	})
	t.Run("space named default is ignored", func(t *testing.T) {
		stub(t, "/Users/x/code/Default/.git\n", nil, spacesJSON)
		if got := Resolve(context.Background(), client, "/default-dir", ""); got != "" {
			t.Fatalf("Resolve = %q, want empty", got)
		}
	})

	before := atomic.LoadInt32(hits)
	if before > 2 {
		t.Fatalf("/spaces 请求次数 = %d, 预料之外的请求过多", before)
	}
}

func TestDescribeOverride(t *testing.T) {
	srv, _ := spacesServer(t)
	stub(t, "/Users/x/code/MCode/.git\n", nil, spacesJSON)
	client := nmem.New(srv.URL, "nmem_test")

	hit := Describe(context.Background(), client, "/work", "mcode")
	if !hit.FromOverride || !hit.Hit || hit.Space != "mcode" || hit.ID != "mcode" {
		t.Fatalf("override 命中 = %+v, want {Space:mcode ID:mcode FromOverride:true Hit:true}", hit)
	}

	miss := Describe(context.Background(), client, "/work", "team")
	if !miss.FromOverride || miss.Hit || miss.Space != "team" || miss.ID != "" {
		t.Fatalf("override 未命中 = %+v, want {Space:team FromOverride:true Hit:false}", miss)
	}
}

func TestDescribeRepoName(t *testing.T) {
	srv, _ := spacesServer(t)
	stub(t, "/Users/x/code/MCode/.git\n", nil, spacesJSON)
	client := nmem.New(srv.URL, "nmem_test")

	hit := Describe(context.Background(), client, "/work", "")
	if hit.FromOverride || !hit.Hit || hit.Space != "mcode" || hit.ID != "mcode" {
		t.Fatalf("仓库名命中 = %+v, want {Space:mcode ID:mcode Hit:true}", hit)
	}

	stub(t, "/Users/x/code/other/.git\n", nil, spacesJSON)
	miss := Describe(context.Background(), client, "/other", "")
	if miss.FromOverride || miss.Hit || miss.Space != "other" || miss.ID != "" {
		t.Fatalf("仓库名未命中 = %+v, want {Space:other Hit:false}", miss)
	}
}

// TestDescribeNilClient 验证没有客户端时只回候选名，不报错也不命中。
func TestDescribeNilClient(t *testing.T) {
	stub(t, "/Users/x/code/MCode/.git\n", nil, spacesJSON)
	got := Describe(context.Background(), nil, "/work", "")
	if got.Hit || got.ID != "" || got.Space != "mcode" {
		t.Fatalf("nil client = %+v, want 候选名 mcode", got)
	}
}
