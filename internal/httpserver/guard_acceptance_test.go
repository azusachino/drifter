package httpserver

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

func TestExpectedTargetBoundaryRejectionsPreserveState(t *testing.T) {
	now := time.Now()
	source, target := &sourceStub{now: now}, &targetStub{}
	db := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	seed := companion.Mapping{Provider: "weread", SourceAccount: "src", BookID: "123", TargetAccount: "dst", WorkID: 7, EditionID: 8}
	if err := store.Confirm(context.Background(), seed, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	server, base, _, _ := newTestServer(t, source, db, now, target)
	sourceFactories, targetFactories := 0, 0
	server.source, server.target = nil, nil
	server.sourceFactory = func() (companion.Source, error) { sourceFactories++; return source, nil }
	server.targetFactory = func() (companion.Target, error) { targetFactories++; return target, nil }
	token := csrfToken(t, base)
	before := listHTTPMappings(t, base)

	for _, action := range []string{"replace", "unlink"} {
		method := http.MethodPost
		baseJSON := `{"source_account":"src","book_id":"123","target_account":"dst"`
		baseForm := url.Values{"csrf_token": {token}, "source_account": {"src"}, "book_id": {"123"}, "target_account": {"dst"}}
		if action == "replace" {
			baseJSON += `,"work_id":9,"edition_id":0`
			baseForm.Set("work_id", "9")
			baseForm.Set("edition_mode", "work_only")
			baseForm.Set("edition_id", "")
			if _, err := mappingFromForm(baseForm); err != nil {
				t.Fatalf("invalid baseline form: %v", err)
			}
		} else {
			method = http.MethodDelete
		}
		validGuard := `,"expected_work_id":7,"expected_edition_id":8`
		cases := []struct{ name, guard string }{
			{"missing work", `,"expected_edition_id":8`},
			{"missing edition", `,"expected_work_id":7`},
			{"null work", `,"expected_work_id":null,"expected_edition_id":8`},
			{"null edition", `,"expected_work_id":7,"expected_edition_id":null`},
			{"zero work", `,"expected_work_id":0,"expected_edition_id":8`},
			{"negative work", `,"expected_work_id":-1,"expected_edition_id":8`},
			{"negative edition", `,"expected_work_id":7,"expected_edition_id":-1`},
			{"string work", `,"expected_work_id":"7","expected_edition_id":8`},
			{"string edition", `,"expected_work_id":7,"expected_edition_id":"8"`},
			{"unknown guard", validGuard + `,"expected_target_id":7`},
			{"duplicate work", validGuard + `,"expected_work_id":7`},
			{"duplicate edition", validGuard + `,"expected_edition_id":8`},
		}
		for _, tc := range cases {
			t.Run("JSON/"+action+"/"+tc.name, func(t *testing.T) {
				response := request(t, base, method, "/api/mappings/"+action, mutationHeaders(base, token), strings.NewReader(baseJSON+tc.guard+`}`))
				if response.StatusCode != http.StatusBadRequest {
					t.Fatalf("status=%d", response.StatusCode)
				}
			})
		}
		for _, field := range []string{"expected_work_id", "expected_edition_id"} {
			for _, invalid := range []string{"missing", "empty", "null", "negative", "duplicate", "unknown", "zero work"} {
				if invalid == "zero work" && field != "expected_work_id" {
					continue
				}
				t.Run("form/"+action+"/"+field+"/"+invalid, func(t *testing.T) {
					form := url.Values{}
					for key, values := range baseForm {
						form[key] = append([]string(nil), values...)
					}
					form.Set("expected_work_id", "7")
					form.Set("expected_edition_id", "8")
					switch invalid {
					case "missing":
						form.Del(field)
					case "empty":
						form.Set(field, "")
					case "null":
						form.Set(field, "null")
					case "negative":
						form.Set(field, "-1")
					case "duplicate":
						form.Add(field, form.Get(field))
					case "unknown":
						form.Set("expected_target_id", "7")
					case "zero work":
						form.Set(field, "0")
					}
					headers := map[string]string{"Origin": base, "Content-Type": "application/x-www-form-urlencoded"}
					response := request(t, base, http.MethodPost, "/mappings/"+action, headers, strings.NewReader(form.Encode()))
					if response.StatusCode != http.StatusBadRequest {
						t.Fatalf("status=%d", response.StatusCode)
					}
				})
			}
		}
	}
	if sourceFactories != 0 || targetFactories != 0 || source.bookCalls.Load() != 0 || target.calls.Load() != 0 {
		t.Fatalf("rejections constructed/called providers: factories=%d/%d calls=%d/%d", sourceFactories, targetFactories, source.bookCalls.Load(), target.calls.Load())
	}
	if after := listHTTPMappings(t, base); !reflect.DeepEqual(before, after) {
		t.Fatalf("state changed: before=%+v after=%+v", before, after)
	}
}
