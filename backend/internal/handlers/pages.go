package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// Paged listings: GET /api/files?limit=N[&cursor=...][&sort=...][&dir=...]
// [&visibility=...] and the same with deleted=true for the trash.
//
// Without limit the endpoint answers as it always has, with every file in one
// array: API clients that use it keep working. With limit it answers a
// filePage. The dashboard pages because an account holding tens of thousands
// of files made the old answer tens of megabytes of JSON, rendered at once.

const (
	defaultPageSize = 100
	maxPageSize     = 500
)

// Sort orders a page can come in. "recent" is newest first only; the others
// take dir=asc|desc.
const (
	sortRecent  = "recent"
	sortName    = "name"
	sortSuccess = "success"
	sortFailure = "failure"
)

// pageRequest is a validated page query.
type pageRequest struct {
	trash      bool
	sort       string
	desc       bool
	visibility string // all | public | private
	limit      int64
	after      pageCursor
}

// pageCursor names the last row of the previous page: its id and its sort
// key (name for a name sort, the counter for a count sort, deleted_at as unix
// seconds for the trash). It also records the listing it belongs to, so a
// cursor from one sort cannot be replayed against another and silently skip
// rows. ID 0 is "no cursor": ids start at 1.
type pageCursor struct {
	Trash      bool   `json:"t,omitempty"`
	Sort       string `json:"s"`
	Desc       bool   `json:"d,omitempty"`
	Visibility string `json:"v"`
	ID         int64  `json:"i"`
	Name       string `json:"n,omitempty"`
	N          int64  `json:"k,omitempty"`
}

// parsePageRequest reads a paged listing's query. It returns ok=false when no
// limit was given, i.e. for the unpaged answer.
func parsePageRequest(q url.Values) (pageRequest, bool, error) {
	if q.Get("limit") == "" {
		return pageRequest{}, false, nil
	}
	limit, err := strconv.ParseInt(q.Get("limit"), 10, 64)
	if err != nil || limit < 1 {
		return pageRequest{}, true, errors.New("limit must be a positive integer")
	}
	req := pageRequest{
		trash:      q.Get("deleted") == "true",
		sort:       q.Get("sort"),
		desc:       q.Get("dir") == "desc",
		visibility: q.Get("visibility"),
		limit:      min(limit, maxPageSize),
	}
	if req.sort == "" {
		req.sort = sortRecent
	}
	// Without dir, each sort runs the way the dashboard starts it: names A to
	// Z, counters highest first.
	if q.Get("dir") == "" && (req.sort == sortSuccess || req.sort == sortFailure) {
		req.desc = true
	}
	if d := q.Get("dir"); d != "" && d != "asc" && d != "desc" {
		return pageRequest{}, true, errors.New("dir must be asc or desc")
	}
	if req.visibility == "" {
		req.visibility = "all"
	}
	switch {
	case req.trash:
		// The trash has one order, most recently deleted first, and no filter.
		req.sort, req.desc, req.visibility = sortRecent, true, "all"
	case req.sort == sortRecent:
		req.desc = true
	case req.sort != sortName && req.sort != sortSuccess && req.sort != sortFailure:
		return pageRequest{}, true, errors.New("sort must be recent, name, success or failure")
	}
	if req.visibility != "all" && req.visibility != "public" && req.visibility != "private" {
		return pageRequest{}, true, errors.New("visibility must be all, public or private")
	}

	if raw := q.Get("cursor"); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &req.after) != nil || req.after.ID < 1 {
			return pageRequest{}, true, errors.New("cursor is not one this server issued")
		}
		if req.after.Trash != req.trash || req.after.Sort != req.sort ||
			req.after.Desc != req.desc || req.after.Visibility != req.visibility {
			return pageRequest{}, true, errors.New("cursor belongs to a different listing; start from the first page")
		}
	}
	return req, true, nil
}

// cursorAfter is the cursor naming f as the last row of a page of req.
func (req pageRequest) cursorAfter(f sqlcgen.File) string {
	c := pageCursor{Trash: req.trash, Sort: req.sort, Desc: req.desc, Visibility: req.visibility, ID: f.ID}
	switch {
	case req.trash:
		c.N = f.DeletedAt.Time.Unix()
	case req.sort == sortName:
		c.Name = f.Name
	case req.sort == sortSuccess:
		c.N = f.SuccessCount
	case req.sort == sortFailure:
		c.N = f.FailureCount
	}
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

// listPage runs the query for req, asking for one row more than the page so
// it knows whether another page follows. Every query is owner-scoped.
func listPage(ctx context.Context, q *sqlcgen.Queries, userID int64, req pageRequest) ([]sqlcgen.File, string, error) {
	size := req.limit + 1
	a := req.after
	var (
		rows []sqlcgen.File
		err  error
	)
	switch {
	case req.trash:
		rows, err = q.PageUserTrash(ctx, sqlcgen.PageUserTrashParams{UserID: userID, AfterID: a.ID, AfterN: a.N, PageSize: size})
	case req.sort == sortRecent:
		rows, err = q.PageUserFilesRecent(ctx, sqlcgen.PageUserFilesRecentParams{UserID: userID, Visibility: req.visibility, AfterID: a.ID, PageSize: size})
	case req.sort == sortName && req.desc:
		rows, err = q.PageUserFilesNameDesc(ctx, sqlcgen.PageUserFilesNameDescParams{UserID: userID, Visibility: req.visibility, AfterID: a.ID, AfterName: a.Name, PageSize: size})
	case req.sort == sortName:
		rows, err = q.PageUserFilesNameAsc(ctx, sqlcgen.PageUserFilesNameAscParams{UserID: userID, Visibility: req.visibility, AfterID: a.ID, AfterName: a.Name, PageSize: size})
	case req.sort == sortSuccess && req.desc:
		rows, err = q.PageUserFilesSuccessDesc(ctx, sqlcgen.PageUserFilesSuccessDescParams{UserID: userID, Visibility: req.visibility, AfterID: a.ID, AfterN: a.N, PageSize: size})
	case req.sort == sortSuccess:
		rows, err = q.PageUserFilesSuccessAsc(ctx, sqlcgen.PageUserFilesSuccessAscParams{UserID: userID, Visibility: req.visibility, AfterID: a.ID, AfterN: a.N, PageSize: size})
	case req.sort == sortFailure && req.desc:
		rows, err = q.PageUserFilesFailureDesc(ctx, sqlcgen.PageUserFilesFailureDescParams{UserID: userID, Visibility: req.visibility, AfterID: a.ID, AfterN: a.N, PageSize: size})
	default:
		rows, err = q.PageUserFilesFailureAsc(ctx, sqlcgen.PageUserFilesFailureAscParams{UserID: userID, Visibility: req.visibility, AfterID: a.ID, AfterN: a.N, PageSize: size})
	}
	if err != nil {
		return nil, "", err
	}
	next := ""
	if int64(len(rows)) > req.limit {
		rows = rows[:req.limit]
		next = req.cursorAfter(rows[len(rows)-1])
	}
	return rows, next, nil
}

// fileCounts is how many files each of the dashboard's views holds.
type fileCounts struct {
	All     int64 `json:"all"`
	Public  int64 `json:"public"`
	Private int64 `json:"private"`
	Trash   int64 `json:"trash"`
}

// filePage is a paged listing's answer. NextCursor is "" on the last page.
type filePage struct {
	Files      []fileResponse `json:"files"`
	NextCursor string         `json:"next_cursor"`
	Counts     fileCounts     `json:"counts"`
}
