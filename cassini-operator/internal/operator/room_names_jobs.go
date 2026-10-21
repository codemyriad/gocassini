package operator

import (
	"context"
	"strings"
)

// Operator job responses keep the captured Talk name and add a display field.
// A pending job can use its captured members before it has a portable room ID.
func (rt *Runtime) projectJobRoomNames(ctx context.Context, jobs []Job) {
	for i := range jobs {
		if jobs[i].TalkBinding != nil {
			state, err := decodeTalkBinding(*jobs[i].TalkBinding)
			if err == nil {
				jobs[i].RoomDisplayName, _ = roomDefault(talkRoomInfo{Name: state.RoomName, Type: state.RoomType, TitleParticipants: state.TitleParticipants})
			}
		}
	}
	if rt.meetingMetadata == nil {
		return
	}
	for start := 0; start < len(jobs); start += 200 {
		end := min(start+200, len(jobs))
		args := []any{}
		marks := []string{}
		positions := map[string]int{}
		for i := start; i < end; i++ {
			for _, ext := range []string{".opus", ".json"} {
				name := jobs[i].ID + ext
				args = append(args, name)
				marks = append(marks, "?")
				positions[name] = i
			}
		}
		rows, err := rt.meetingMetadata.db.QueryContext(ctx, `SELECT m.opus_name,
 COALESCE(r.custom_name,NULLIF(r.default_name,''),r.room_id)
 FROM meeting_metadata m JOIN room_names r ON r.room_id=json_extract(m.entry_json,'$.roomId')
 WHERE m.opus_name IN (`+strings.Join(marks, ",")+`) ORDER BY m.indexed_at,m.file_id`, args...)
		if err != nil {
			return
		}
		for rows.Next() {
			var name, display string
			if rows.Scan(&name, &display) == nil {
				jobs[positions[name]].RoomDisplayName = display
			}
		}
		rows.Close()
	}
}
