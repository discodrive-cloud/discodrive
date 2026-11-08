package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"discodrive/internal/dav"
)

type taskHarness struct {
	mux        *http.ServeMux
	dav        *dav.Service
	tokA, tokB string
	uidA       string
}

func newTaskHarness(t *testing.T) *taskHarness {
	t.Helper()
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	tokA, _, err := svc.Register(ctx, "a@x.test", "password12")
	if err != nil {
		t.Fatalf("register a: %v", err)
	}
	tokB, _, err := svc.Register(ctx, "b@x.test", "password12")
	if err != nil {
		t.Fatalf("register b: %v", err)
	}
	d := dav.NewService(pool)
	s := &Server{auth: svc, q: q, dav: d}
	mux := http.NewServeMux()
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /me/tasks":              s.handleListTasks,
		"POST /me/tasks":             s.handleCreateTask,
		"GET /me/tasks/{uid}":        s.handleGetTask,
		"PUT /me/tasks/{uid}":        s.handleUpdateTask,
		"PUT /me/tasks/{uid}/done":   s.handleToggleTask,
		"DELETE /me/tasks/{uid}":     s.handleDeleteTask,
		"GET /me/task-lists":         s.handleListTaskLists,
		"POST /me/task-lists":        s.handleCreateTaskList,
		"PATCH /me/task-lists/{id}":  s.handleUpdateTaskList,
		"DELETE /me/task-lists/{id}": s.handleDeleteTaskList,
	} {
		mux.Handle(pattern, svc.Middleware(h))
	}
	return &taskHarness{mux: mux, dav: d, tokA: tokA, tokB: tokB, uidA: mustUserID(t, svc, tokA)}
}

func (h *taskHarness) do(t *testing.T, tok, method, path string, body any, out any) int {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytesReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if out != nil {
		_ = json.Unmarshal(rec.Body.Bytes(), out)
	}
	return rec.Code
}

func (h *taskHarness) lists(t *testing.T, tok string) []taskListDTO {
	t.Helper()
	var out []taskListDTO
	if code := h.do(t, tok, "GET", "/me/task-lists", nil, &out); code != http.StatusOK {
		t.Fatalf("GET /me/task-lists: %d", code)
	}
	return out
}

func (h *taskHarness) tasks(t *testing.T, tok, query string) []taskForm {
	t.Helper()
	var out []taskForm
	if code := h.do(t, tok, "GET", "/me/tasks"+query, nil, &out); code != http.StatusOK {
		t.Fatalf("GET /me/tasks%s: %d", query, code)
	}
	return out
}

func TestTaskListsCreateAndFilter(t *testing.T) {
	h := newTaskHarness(t)
	lists := h.lists(t, h.tokA)
	if len(lists) != 1 || !lists[0].IsDefault {
		t.Fatalf("a new user gets one default list: %+v", lists)
	}
	defID := lists[0].ID

	var shop taskListDTO
	if code := h.do(t, h.tokA, "POST", "/me/task-lists", map[string]string{"name": " Покупки ", "color": "#34d399"}, &shop); code != http.StatusCreated {
		t.Fatalf("create list: %d", code)
	}
	if shop.Name != "Покупки" || shop.IsDefault {
		t.Fatalf("created list: %+v", shop)
	}
	lists = h.lists(t, h.tokA)
	if len(lists) != 2 || lists[0].ID != defID || !lists[0].IsDefault || lists[1].IsDefault {
		t.Fatalf("the old list stays the default: %+v", lists)
	}

	var created map[string]string
	h.do(t, h.tokA, "POST", "/me/tasks", map[string]any{"summary": "Молоко", "list_id": shop.ID}, &created)
	if created["list_id"] != shop.ID {
		t.Fatalf("task went to %q, want %q", created["list_id"], shop.ID)
	}
	h.do(t, h.tokA, "POST", "/me/tasks", map[string]any{"summary": "Отчёт"}, &created)
	if created["list_id"] != defID {
		t.Fatalf("a task without list_id goes to the default list, got %q", created["list_id"])
	}

	all := h.tasks(t, h.tokA, "")
	if len(all) != 2 {
		t.Fatalf("all lists: %+v", all)
	}
	for _, task := range all {
		want := defID
		if task.Summary == "Молоко" {
			want = shop.ID
		}
		if task.ListID != want {
			t.Fatalf("%q: list_id %q, want %q", task.Summary, task.ListID, want)
		}
	}
	only := h.tasks(t, h.tokA, "?list="+shop.ID)
	if len(only) != 1 || only[0].Summary != "Молоко" {
		t.Fatalf("filter by list: %+v", only)
	}

	if code := h.do(t, h.tokA, "PATCH", "/me/task-lists/"+shop.ID, map[string]string{"name": "Магазин", "color": "#f43f5e"}, nil); code != http.StatusOK {
		t.Fatalf("rename: %d", code)
	}
	for _, l := range h.lists(t, h.tokA) {
		if l.ID == shop.ID && (l.Name != "Магазин" || l.Color != "#f43f5e") {
			t.Fatalf("rename/color not saved: %+v", l)
		}
	}
}

func TestTaskMoveAndUIDOnlyAccess(t *testing.T) {
	h := newTaskHarness(t)
	defID := h.lists(t, h.tokA)[0].ID
	var shop taskListDTO
	h.do(t, h.tokA, "POST", "/me/task-lists", map[string]string{"name": "Покупки"}, &shop)

	var created map[string]string
	h.do(t, h.tokA, "POST", "/me/tasks", map[string]any{"summary": "Хлеб", "list_id": shop.ID}, &created)
	uid := created["uid"]

	// clients that know only the uid (MCP) still find a task outside the default list
	var got taskForm
	if code := h.do(t, h.tokA, "GET", "/me/tasks/"+uid, nil, &got); code != http.StatusOK || got.ListID != shop.ID {
		t.Fatalf("get by uid: %d %+v", code, got)
	}
	if code := h.do(t, h.tokA, "PUT", "/me/tasks/"+uid+"/done", map[string]bool{"completed": true}, nil); code != http.StatusOK {
		t.Fatalf("toggle by uid: %d", code)
	}
	if code := h.do(t, h.tokA, "GET", "/me/tasks/"+uid+"?list="+defID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("the task is not in the default list: %d", code)
	}

	// move to the default list, keeping the edited fields
	if code := h.do(t, h.tokA, "PUT", "/me/tasks/"+uid+"?list="+shop.ID, map[string]any{"summary": "Хлеб чёрный", "completed": true, "list_id": defID}, nil); code != http.StatusOK {
		t.Fatalf("move: %d", code)
	}
	if n := len(h.tasks(t, h.tokA, "?list="+shop.ID)); n != 0 {
		t.Fatalf("the original must be gone from the old list, %d left", n)
	}
	moved := h.tasks(t, h.tokA, "?list="+defID)
	if len(moved) != 1 || moved[0].UID != uid || moved[0].Summary != "Хлеб чёрный" || !moved[0].Completed {
		t.Fatalf("moved task: %+v", moved)
	}

	if code := h.do(t, h.tokA, "DELETE", "/me/tasks/"+uid, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete by uid: %d", code)
	}
}

func TestTaskListsArePrivate(t *testing.T) {
	h := newTaskHarness(t)
	var shop taskListDTO
	h.do(t, h.tokA, "POST", "/me/task-lists", map[string]string{"name": "Покупки"}, &shop)
	var created map[string]string
	h.do(t, h.tokA, "POST", "/me/tasks", map[string]any{"summary": "Сыр", "list_id": shop.ID}, &created)

	if code := h.do(t, h.tokB, "GET", "/me/tasks?list="+shop.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("B lists A's list: %d", code)
	}
	if code := h.do(t, h.tokB, "POST", "/me/tasks", map[string]any{"summary": "x", "list_id": shop.ID}, nil); code != http.StatusNotFound {
		t.Fatalf("B creates in A's list: %d", code)
	}
	if code := h.do(t, h.tokB, "GET", "/me/tasks/"+created["uid"], nil, nil); code != http.StatusNotFound {
		t.Fatalf("B reads A's task: %d", code)
	}
	if code := h.do(t, h.tokB, "PATCH", "/me/task-lists/"+shop.ID, map[string]string{"name": "x"}, nil); code != http.StatusNotFound {
		t.Fatalf("B renames A's list: %d", code)
	}
	if code := h.do(t, h.tokB, "DELETE", "/me/task-lists/"+shop.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("B deletes A's list: %d", code)
	}
	// moving a task into someone else's list is refused too
	bDef := h.lists(t, h.tokB)[0].ID
	if code := h.do(t, h.tokA, "PUT", "/me/tasks/"+created["uid"], map[string]any{"summary": "Сыр", "list_id": bDef}, nil); code != http.StatusNotFound {
		t.Fatalf("A moves into B's list: %d", code)
	}
}

func TestTaskListDelete(t *testing.T) {
	h := newTaskHarness(t)
	defID := h.lists(t, h.tokA)[0].ID
	if code := h.do(t, h.tokA, "DELETE", "/me/task-lists/"+defID, nil, nil); code != http.StatusConflict {
		t.Fatalf("the last list must stay: %d", code)
	}
	var shop taskListDTO
	h.do(t, h.tokA, "POST", "/me/task-lists", map[string]string{"name": "Покупки"}, &shop)
	h.do(t, h.tokA, "POST", "/me/tasks", map[string]any{"summary": "Сыр", "list_id": shop.ID}, nil)
	if code := h.do(t, h.tokA, "DELETE", "/me/task-lists/"+shop.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete list: %d", code)
	}
	if n := len(h.tasks(t, h.tokA, "")); n != 0 {
		t.Fatalf("the list's tasks go with it, %d left", n)
	}
	// a list created by Reminders (MKCALENDAR) shows up as well
	if _, err := h.dav.CreateCalendarWithURI(context.Background(), h.uidA, "ABC-1", "Дом", "VTODO"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.lists(t, h.tokA)); n != 2 {
		t.Fatalf("a Reminders-created list must be listed: %d", n)
	}
}
