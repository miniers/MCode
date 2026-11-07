package harness

import (
	"reflect"
	"testing"
)

func TestProcessRootsTracksMultiplePIDsPerThread(t *testing.T) {
	var roots ProcessRoots
	roots.Set("t1", 100)
	roots.Set("t1", 200)
	roots.Set("t1", 100) // 重复登记无副作用
	roots.Set("t2", 300)

	if got := roots.PIDs("t1"); !reflect.DeepEqual(got, []int{100, 200}) {
		t.Fatalf("t1 的 PID = %v，期望 [100 200]", got)
	}

	// 只清除指定 PID，同一会话的其他进程记录保留。
	roots.Clear("t1", 100)
	if got := roots.PIDs("t1"); !reflect.DeepEqual(got, []int{200}) {
		t.Fatalf("清除 100 后 t1 的 PID = %v，期望 [200]", got)
	}
	roots.Clear("t1", 999) // 不存在的 PID 不影响现有记录
	if got := roots.PIDs("t1"); !reflect.DeepEqual(got, []int{200}) {
		t.Fatalf("清除不存在的 PID 后 t1 的 PID = %v，期望 [200]", got)
	}
	roots.Clear("t1", 200)
	if got := roots.PIDs("t1"); got != nil {
		t.Fatalf("全部清除后 t1 的 PID = %v，期望 nil", got)
	}

	// pid <= 0 清空整个会话。
	roots.Set("t2", 301)
	roots.Clear("t2", 0)
	if got := roots.PIDs("t2"); got != nil {
		t.Fatalf("pid<=0 应清空会话，实际 %v", got)
	}

	// 返回的是副本，调用方改动不影响内部记录。
	roots.Set("t3", 1)
	got := roots.PIDs("t3")
	got[0] = 42
	if again := roots.PIDs("t3"); !reflect.DeepEqual(again, []int{1}) {
		t.Fatalf("PIDs 应返回副本，实际 %v", again)
	}
}
