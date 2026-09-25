package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestExistingTagInheritsRestyledAppearance(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprintf("bulk=%v", bulk), func(t *testing.T) {
			s, h, store, nc := batchFixture(t)
			first := postAsync(t, h, `{"ops":[{"op":"mark","tag":{"label":"portable"},"target":{"kind":"meeting"}}],"tagStyles":[{"label":"portable","color":"slate"}]}`)
			var doc projectedDocument
			if err := json.Unmarshal(first.Annotations, &doc); err != nil {
				t.Fatal(err)
			}
			id := doc.Tags[0].ID
			decodeTagEdit(t, tagCall(h, http.MethodPost, "/"+id, "alice", `{"color":"purple","icon":"star"}`))
			if job := waitTagJob(t, s, "alice"); len(job.Failed) != 0 {
				t.Fatalf("restyle: %+v", job)
			}
			// More hidden carriers must not influence the copied appearance.
			hidden, err := store.document(context.Background(), "MEETING1.opus")
			if err != nil {
				t.Fatal(err)
			}
			hidden.Annotations = json.RawMessage(strings.ReplaceAll(string(hidden.Annotations), `"purple"`, `"red"`))
			for i := 0; i < 3; i++ {
				recordMarks(t, store, fmt.Sprintf("HIDDEN%d.opus", i), hidden)
			}
			// A conflicting client creation hint must not override the visible
			// existing identity's saved appearance.
			ops := fmt.Sprintf(`"ops":[{"op":"mark","tag":{"id":%q,"label":"portable"},"target":{"kind":"meeting"}}],"tagStyles":[{"label":"portable","color":"orange"}]`, id)
			var status int
			if bulk {
				r := batchCall(h, `{"meetingIds":["SECRET"],"requestId":"inherit",`+ops+`}`)
				status = r.Code
			} else {
				r := annTestCall(h, http.MethodPost, "SECRET", "alice", `{`+ops+`}`)
				status = r.Code
			}
			if status != 200 {
				t.Fatalf("mark returned %d", status)
			}
			// Copy to a third meeting after the second has entered the vocabulary.
			empty := annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000}
			data, _ := json.Marshal(empty)
			nc.seed("CassiniRecordings/meetings/THIRD.opus", string(data), nil)
			recordMarks(t, store, "THIRD.opus", empty)
			var third annotationBatchRequest
			if err := json.Unmarshal([]byte(`{"meetingIds":["THIRD"],"requestId":"third",`+ops+`}`), &third); err != nil {
				t.Fatal(err)
			}
			if _, err := s.commitAnnotationBatch(context.Background(), "alice", third,
				map[string]string{"THIRD": "CassiniRecordings/meetings/THIRD.opus"},
				map[string]string{"THIRD": "THIRD.opus"},
				[]string{"MEETING1.opus", "SECRET.opus", "THIRD.opus"}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"MEETING1.opus", "SECRET.opus", "THIRD.opus"} {
				result, err := store.document(context.Background(), name)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(result.Annotations, &doc); err != nil {
					t.Fatal(err)
				}
				tag := doc.Tags[0]
				if tag.ID != id || tag.Color == nil || *tag.Color != "purple" || tag.Icon == nil || *tag.Icon != "star" {
					t.Fatalf("%s did not keep purple/star: %s", name, result.Annotations)
				}
				if err := s.syncAnnotation(context.Background(), name); err != nil {
					t.Fatal(err)
				}
				var archived annotateResult
				if err := json.Unmarshal([]byte(nc.recording("CassiniRecordings/meetings/"+name)), &archived); err != nil {
					t.Fatal(err)
				}
				if !sameAnnotationDocument(result.Annotations, archived.Annotations) {
					t.Fatal("archive lost inherited appearance")
				}
			}
			// Applying the tag again must preserve a deliberately different local style.
			r := annTestCall(h, http.MethodPost, "SECRET", "alice", fmt.Sprintf(`{"ops":[{"op":"restyle","tagId":%q,"color":"teal","icon":""}]}`, id))
			if r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			r = annTestCall(h, http.MethodPost, "SECRET", "alice", `{`+ops+`}`)
			if r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			result, _ := store.document(context.Background(), "SECRET.opus")
			json.Unmarshal(result.Annotations, &doc)
			if *doc.Tags[0].Color != "teal" || *doc.Tags[0].Icon != "" {
				t.Fatal("existing local appearance overwritten")
			}
		})
	}
}
