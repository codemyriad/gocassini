package operator

import "context"

// Topic suggestions ship as an unannounced preview (docs/topic-suggestions.md,
// "Unannounced preview"): locked until someone with a shell in the container
// runs `cassini-operator topics-preview on`. While locked they behave as if
// they were switched off and Settings does not show them; the Settings switch
// and every topic row are kept, so unlocking again picks up where it was.
//
// The flag lives in topic_meta rather than settings.json because the command
// runs beside a serving operator, which holds settings.json in memory and
// would write it back over the command's change.
const topicMetaPreviewUnlocked = "preview_unlocked"

// topicsPreviewUnlocked reads the flag; unset, or any failure, is locked.
func (s *annotationStore) topicsPreviewUnlocked(ctx context.Context) bool {
	if s == nil {
		return false
	}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM topic_meta WHERE key=?`, topicMetaPreviewUnlocked).Scan(&value)
	return err == nil && value == "1"
}

func (s *topicsStore) setTopicsPreviewUnlocked(ctx context.Context, unlocked bool) error {
	value := "0"
	if unlocked {
		value = "1"
	}
	return s.setMeta(ctx, topicMetaPreviewUnlocked, value)
}

// topicsUnlocked says whether the preview is unlocked here. It reads the
// database on every call, so the command takes effect without a restart.
func (rt *Runtime) topicsUnlocked(ctx context.Context) bool {
	return rt.annotationReads().topicsPreviewUnlocked(ctx)
}

// topicSuggestionsOn is the one gate for topic suggestions: unlocked here and
// switched on in Settings. Every route, the worker, the publish hook and the
// model check ask it rather than reading the Settings switch.
func (rt *Runtime) topicSuggestionsOn(ctx context.Context) bool {
	return rt.currentSettings().TopicSuggestions.Enabled && rt.topicsUnlocked(ctx)
}

// withoutTopicsModels is the Settings inventory with no embedding model and
// none of its jobs, while the preview is locked. Only the listing: an
// installed model keeps its install job and its checks.
func withoutTopicsModels(models []modelInfo, jobs []modelJob) ([]modelInfo, []modelJob) {
	hidden := map[string]bool{defaultEmbeddingModel: true}
	kept := make([]modelInfo, 0, len(models))
	for _, m := range models {
		if m.Kind == modelKindEmbedding {
			hidden[m.ID] = true
			continue
		}
		kept = append(kept, m)
	}
	keptJobs := make([]modelJob, 0, len(jobs))
	for _, j := range jobs {
		if !hidden[j.Model] {
			keptJobs = append(keptJobs, j)
		}
	}
	return kept, keptJobs
}
