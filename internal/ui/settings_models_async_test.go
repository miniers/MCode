package mcodeui

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func modelsAsyncRowTester(t *testing.T, client Client, pins []ModelPin) (*ui.Tester, *SettingsState, *modelsPageState) {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
	ps := modelsPageStateOf(st)
	ps.loaded, ps.seeded = true, true
	ps.pins = append([]ModelPin(nil), pins...)
	ps.syncSettings(st)
	tt := ui.NewTester(func(c *ui.Context) {
		ps.applyOperations()
		renderProviderModelRow(c, st, ps, client, ProviderInfo{ID: "claude"}, ModelInfo{ID: "sonnet", Name: "Sonnet"})
	}, 800, 500)
	return tt, st, ps
}

func TestSettingsModelsAsyncAddBusy(t *testing.T) {
	for _, phase := range []string{"preferences", "add"} {
		t.Run(phase, func(t *testing.T) {
			testenv.TempHome(t)
			fake := NewFakeClient()
			pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var calls atomic.Int32
			block := func(ctx context.Context) error {
				calls.Add(1)
				if _, ok := ctx.Deadline(); !ok {
					return errors.New("missing RPC deadline")
				}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			client := modelsPageTestClient{Client: fake}
			if phase == "preferences" {
				client.preferences = func(ctx context.Context, pin ModelPin) (ModelPreference, error) {
					if err := block(ctx); err != nil {
						return ModelPreference{}, err
					}
					return fake.ModelPreferences(ctx, pin)
				}
			} else {
				client.add = func(ctx context.Context, pin ModelPin) (bool, error) {
					if err := block(ctx); err != nil {
						return false, err
					}
					return fake.AddModelToList(ctx, pin)
				}
			}
			tt, st, ps := modelsAsyncRowTester(t, client, nil)
			if err := tt.Click("models-add-claude/sonnet"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool { return calls.Load() == 1 })
			if node, ok := tt.Access("models-add-claude/sonnet"); !ok || !node.Disabled {
				t.Fatal("pending add must disable its button")
			}
			if err := tt.Click("models-add-claude/sonnet"); err != nil {
				t.Fatal(err)
			}
			magpieAddModelPin(st, ps, client, pin)
			if calls.Load() != 1 || !ps.pinBusy(pin) {
				t.Fatal("duplicate add must not start a second RPC")
			}
			unblock()
			waitFor(t, tt, func() bool { return !ps.pinBusy(pin) })
			if !modelPinPresent(ps.pins, pin) || !modelPinPresent(st.cfg.Models.Pins, pin) || ps.opErr != "" {
				t.Fatalf("add result was not applied: pins=%v error=%q", ps.pins, ps.opErr)
			}
		})
	}
}

func TestSettingsModelsAsyncRemoveFailure(t *testing.T) {
	testenv.TempHome(t)
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var calls atomic.Int32
	client := modelsPageTestClient{Client: NewFakeClient(), remove: func(ctx context.Context, _ ModelPin) (bool, error) {
		calls.Add(1)
		select {
		case <-release:
			return false, errors.New("disk full")
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}}
	tt, st, ps := modelsAsyncRowTester(t, client, []ModelPin{pin})
	if err := tt.Click("models-added-claude/sonnet"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return calls.Load() == 1 })
	if node, ok := tt.Access("models-added-claude/sonnet"); !ok || !node.Disabled {
		t.Fatal("pending remove must disable its button")
	}
	ps.removePin(st, client, pin)
	unblock()
	waitFor(t, tt, func() bool { return !ps.pinBusy(pin) })
	if calls.Load() != 1 || ps.opErr != "移除模型失败：disk full" || !modelPinPresent(ps.pins, pin) || !modelPinPresent(st.cfg.Models.Pins, pin) {
		t.Fatalf("failed remove must preserve pins and expose error: pins=%v error=%q", ps.pins, ps.opErr)
	}
}

func TestSettingsModelsAsyncReorderIgnoresOlderResult(t *testing.T) {
	for _, olderErr := range []error{nil, errors.New("old failure")} {
		t.Run(map[bool]string{true: "failure", false: "success"}[olderErr != nil], func(t *testing.T) {
			testenv.TempHome(t)
			pins := []ModelPin{{Provider: "claude", ModelID: "a"}, {Provider: "claude", ModelID: "b"}, {Provider: "claude", ModelID: "c"}}
			type request struct {
				pins []ModelPin
				done chan error
			}
			requests := make(chan request, 2)
			client := modelsPageTestClient{Client: NewFakeClient(), reorder: func(ctx context.Context, pins []ModelPin) error {
				done := make(chan error, 1)
				requests <- request{pins: pins, done: done}
				select {
				case err := <-done:
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			tt, st, ps := modelsAsyncRowTester(t, client, pins)
			ps.reorder(st, client, 0, 2)
			var first, second request
			waitFor(t, tt, func() bool {
				select {
				case first = <-requests:
					return true
				default:
					return false
				}
			})
			ps.reorder(st, client, 0, 1)
			want := []ModelPin{pins[2], pins[1], pins[0]}
			if !reflect.DeepEqual(ps.pins, want) || !reflect.DeepEqual(st.cfg.Models.Pins, want) {
				t.Fatal("local reorder must take effect before RPC completes")
			}
			first.done <- olderErr
			waitModelsOperationQueued(t, ps)
			tt.Frame()
			if !reflect.DeepEqual(ps.pins, want) || ps.opErr != "" || !ps.loaded {
				t.Fatal("older result must not change the latest local order or trigger a reload")
			}
			waitFor(t, tt, func() bool {
				select {
				case second = <-requests:
					return true
				default:
					return false
				}
			})
			if !reflect.DeepEqual(second.pins, want) {
				t.Fatal("second RPC did not capture the latest order")
			}
			second.done <- nil
			waitModelsOperationQueued(t, ps)
			tt.Frame()
			if !reflect.DeepEqual(ps.pins, want) || !reflect.DeepEqual(st.cfg.Models.Pins, want) || ps.opErr != "" || !ps.loaded {
				t.Fatalf("older result overwrote newer state: pins=%v error=%q", ps.pins, ps.opErr)
			}
		})
	}
}

// 此处不推进帧，先确认后台完成；同样覆盖窗口关闭后无人消费的完成队列。
func waitModelsOperationQueued(t *testing.T, ps *modelsPageState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ps.opMu.Lock()
		queued := len(ps.completed) > 0
		ps.opMu.Unlock()
		if queued {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("model operation did not finish")
}

func TestSettingsModelsAsyncReorderFailureReloads(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	pins := []ModelPin{{Provider: "claude", ModelID: "a"}, {Provider: "claude", ModelID: "b"}}
	for _, pin := range pins {
		_, _ = fake.AddModelToList(context.Background(), pin)
	}
	client := modelsPageTestClient{Client: fake, reorder: func(context.Context, []ModelPin) error { return errors.New("disk full") }}
	tt, st := modelsPageTester(t, client, nil)
	ps := modelsPageStateOf(st)
	ps.reorder(st, client, 0, 1)
	waitFor(t, tt, func() bool { return ps.loaded && ps.load == nil && ps.opErr == "保存模型顺序失败：disk full" })
	if !reflect.DeepEqual(ps.pins, pins) || !reflect.DeepEqual(st.cfg.Models.Pins, pins) {
		t.Fatal("failed reorder must reload persisted pins")
	}
}

func TestSettingsModelsAsyncCompletionAfterClose(t *testing.T) {
	testenv.TempHome(t)
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	client := modelsPageTestClient{Client: NewFakeClient(), remove: func(ctx context.Context, _ ModelPin) (bool, error) {
		select {
		case <-release:
			return false, errors.New("closed window")
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}}
	tt, st, ps := modelsAsyncRowTester(t, client, []ModelPin{pin})
	ps.removePin(st, client, pin)
	st.Close()
	unblock()
	waitModelsOperationQueued(t, ps)
	if ps.opErr != "" || !ps.pinBusy(pin) {
		t.Fatal("background completion must not mutate UI state while the window is closed")
	}
	tt.Frame()
	if ps.opErr != "移除模型失败：closed window" || ps.pinBusy(pin) {
		t.Fatal("the next render must consume the pending completion")
	}
}
