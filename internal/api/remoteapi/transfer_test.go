package remoteapi

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/remote"
)

func failureURL(uuid string) string { return "/api/remote/servers/" + uuid + "/transfer/failure" }
func successURL(uuid string) string { return "/api/remote/servers/" + uuid + "/transfer/success" }

func TestTransferFailure(t *testing.T) {
	for _, node := range []uint64{node1, node2} {
		t.Run("reported by the node that "+map[uint64]string{node1: "gives it", node2: "gets it"}[node], func(t *testing.T) {
			r := newRig(t)
			rp := r.post(node, failureURL(movingUUID), "")
			if rp.Status != 204 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			// The allocations of the new node are released and the server stays where it is.
			want := []completeCall{{ServerID: 106, Result: remote.TransferResult{Release: []uint64{2, 4}}}}
			if !reflect.DeepEqual(r.store.completes, want) {
				t.Errorf("completes = %+v, want %+v", r.store.completes, want)
			}
			if len(r.store.oldNodeCalls) != 0 {
				t.Errorf("the old node was told to delete: %v", r.store.oldNodeCalls)
			}
		})
	}
}

func TestTransferSuccess(t *testing.T) {
	r := newRig(t)
	rp := r.post(node2, successURL(movingUUID), "")
	if rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := []completeCall{{ServerID: 106, Result: remote.TransferResult{
		Successful: true, Release: []uint64{1, 3}, Move: true, NewNode: node2, NewAllocation: 2, DeleteFromOldNode: node1,
	}}}
	if !reflect.DeepEqual(r.store.completes, want) {
		t.Errorf("completes = %+v, want %+v", r.store.completes, want)
	}
	if !reflect.DeepEqual(r.store.oldNodeCalls, []string{movingUUID}) {
		t.Errorf("the old node was told to delete %v", r.store.oldNodeCalls)
	}
}

func TestTransferRefusals(t *testing.T) {
	tests := []struct {
		name   string
		node   uint64
		url    string
		status int
		detail string
	}{
		{"success from the old node", node1, successURL(movingUUID), 403, "Requesting node does not have permission to access this server."},
		{"failure from a node that has nothing to do with it", node3, failureURL(movingUUID), 403, ""},
		{"success from a node that has nothing to do with it", node3, successURL(movingUUID), 403, ""},
		{"no transfer, failure", node1, failureURL(normalUUID), 409, "Server is not being transferred."},
		{"no transfer, success", node1, successURL(normalUUID), 409, "Server is not being transferred."},
		{"a server of another node", node1, successURL(otherNodeUUID), 403, ""},
		{"a server that does not exist", node1, failureURL("12345678-2222-4333-8444-555555555555"), 403, ""},
		{"not a UUID", node1, failureURL("106"), 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(tt.node, tt.url, "")
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.detail != "" && rp.Detail() != tt.detail {
				t.Errorf("detail = %q", rp.Detail())
			}
			if len(r.store.completes) != 0 || len(r.store.oldNodeCalls) != 0 {
				t.Error("a transfer was ended or a node told to delete")
			}
		})
	}

	t.Run("a store that finds servers of other nodes is not trusted", func(t *testing.T) {
		r := newRig(t)
		r.store.leak = true
		if rp := r.post(node3, failureURL(movingUUID), ""); rp.Status != 403 {
			t.Errorf("failure from a third node: status = %d", rp.Status)
		}
		if rp := r.post(node1, successURL(movingUUID), ""); rp.Status != 403 {
			t.Errorf("success from the old node: status = %d", rp.Status)
		}
		if len(r.store.completes) != 0 {
			t.Errorf("completes = %+v", r.store.completes)
		}
	})
}

func TestTransferEndedTwice(t *testing.T) {
	r := newRig(t)
	r.store.completeRace = true
	rp := r.post(node2, successURL(movingUUID), "")
	if rp.Status != 409 || rp.Detail() != "Server is not being transferred." {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.oldNodeCalls) != 0 {
		t.Error("the old node was told to delete a server whose transfer another report ended")
	}
}

func TestTransferFailures(t *testing.T) {
	t.Run("the old node does not delete, which is only logged", func(t *testing.T) {
		r := newRig(t)
		r.store.failOldNode = errors.New("node is down")
		rp := r.post(node2, successURL(movingUUID), "")
		if rp.Status != 204 || !strings.Contains(r.logs.String(), "the old node did not delete the server it handed over") {
			t.Errorf("status = %d, logs %s", rp.Status, r.logs.String())
		}
	})
	t.Run("the end cannot be stored", func(t *testing.T) {
		r := newRig(t)
		r.store.failComplete = errBoom
		rp := r.post(node2, successURL(movingUUID), "")
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
		if len(r.store.oldNodeCalls) != 0 {
			t.Error("the old node was told to delete a server whose transfer was not stored")
		}
	})
}

// The node API reaches no server without a node: a route wired without the authorizer is refused, and no store is asked.
func TestHandlersRefuseWithoutANode(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		bare := g.Sub("/bare")
		bare.POST("/sftp", r.handlers.SFTPAuth, api.Public())
		bare.GET("/servers", r.handlers.List, api.Public())
		bare.POST("/reset", r.handlers.Reset, api.Public())
		bare.POST("/activity", r.handlers.Activity, api.Public())
		bare.GET("/servers/:uuid", r.handlers.Details, api.Public())
		bare.GET("/servers/:uuid/install", r.handlers.InstallScript, api.Public())
		bare.POST("/servers/:uuid/install", r.handlers.InstallReport, api.Public())
		bare.POST("/servers/:uuid/transfer/failure", r.handlers.TransferFailure, api.Public())
		bare.POST("/servers/:uuid/transfer/success", r.handlers.TransferSuccess, api.Public())
	})

	for _, rt := range []struct{ method, path, body string }{
		{"POST", "/bare/sftp", passwordBody("ada.aaaa1111", adaPW)}, {"GET", "/bare/servers", ""}, {"POST", "/bare/reset", ""},
		{"POST", "/bare/activity", `{"data":[]}`}, {"GET", "/bare/servers/" + normalUUID, ""}, {"GET", "/bare/servers/" + normalUUID + "/install", ""},
		{"POST", "/bare/servers/" + installingUUID + "/install", report("true", "")},
		{"POST", "/bare/servers/" + movingUUID + "/transfer/failure", ""}, {"POST", "/bare/servers/" + movingUUID + "/transfer/success", ""},
	} {
		if rp := client.As(0, rt.method, rt.path, rt.body); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	s := r.store
	if len(s.listCalls)+len(s.installs)+len(s.resets)+len(s.completes)+len(s.events)+s.storeCalls != 0 {
		t.Error("a store was asked")
	}
}
