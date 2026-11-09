package mcodeui

import (
	"context"
	"testing"
	"time"
)

type queuedMagpieClient struct {
	*FakeClient
	piRequests    chan chan []PiMagpieEndpoint
	agentRequests chan chan AgentMagpieSetting
}

func (c *queuedMagpieClient) PiMagpieEndpoints(context.Context) ([]PiMagpieEndpoint, error) {
	reply := make(chan []PiMagpieEndpoint, 1)
	c.piRequests <- reply
	return <-reply, nil
}

func (c *queuedMagpieClient) AgentMagpie(context.Context, string) (AgentMagpieSetting, error) {
	reply := make(chan AgentMagpieSetting, 1)
	c.agentRequests <- reply
	return <-reply, nil
}

func TestMagpieRefreshQueuesAndDiscardsStaleSnapshot(t *testing.T) {
	for _, agent := range []string{"pi", "claude"} {
		t.Run(agent, func(t *testing.T) {
			client := &queuedMagpieClient{
				FakeClient: NewFakeClient(), piRequests: make(chan chan []PiMagpieEndpoint, 2),
				agentRequests: make(chan chan AgentMagpieSetting, 2),
			}
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
			if agent == "pi" {
				st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "existing"}}
			} else {
				st.agentMagpieFor(agent).setting.Endpoints = []PiMagpieEndpoint{{ID: "existing"}}
			}
			load := func() {
				if agent == "pi" {
					st.loadPiMagpieEndpoints()
				} else {
					st.reloadAgentMagpie(agent)
				}
			}
			next := func() func(string) {
				if agent == "pi" {
					select {
					case reply := <-client.piRequests:
						return func(id string) { reply <- []PiMagpieEndpoint{{ID: id}} }
					case <-time.After(time.Second):
						t.Fatal("pi 请求没有发起")
					}
				} else {
					select {
					case reply := <-client.agentRequests:
						return func(id string) { reply <- AgentMagpieSetting{Endpoints: []PiMagpieEndpoint{{ID: id}}} }
					case <-time.After(time.Second):
						t.Fatal("agent 请求没有发起")
					}
				}
				return nil
			}
			load()
			oldReply := next()
			load()
			oldReply("old")
			newReply := next()
			endpoints, loading, _, _, _ := magpieEndpointsFor(st, agent)
			if !loading || len(endpoints) != 1 || endpoints[0].ID != "existing" {
				newReply("new")
				t.Fatalf("旧响应覆盖了快照: %+v，loading=%v", endpoints, loading)
			}
			newReply("new")
			deadline := time.Now().Add(time.Second)
			for {
				endpoints, loading, _, _, _ = magpieEndpointsFor(st, agent)
				if !loading {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("补拉没有完成")
				}
				time.Sleep(time.Millisecond)
			}
			if len(endpoints) != 1 || endpoints[0].ID != "new" {
				t.Fatalf("补拉快照错误: %+v", endpoints)
			}
		})
	}
}
