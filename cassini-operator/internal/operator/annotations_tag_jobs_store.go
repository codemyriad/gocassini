package operator

import (
	"context"
	"encoding/json"
)

func (s *annotationService) persistTagJob(job *tagJob, targets []string, op json.RawMessage, titles map[string]string) error {
	data, _ := json.Marshal(job)
	names, _ := json.Marshal(targets)
	labels, _ := json.Marshal(titles)
	_, err := s.rt.annotationReads().db.ExecContext(s.rt.ctx, `INSERT INTO annotation_tag_job(caller,job_json,targets,op,titles) VALUES(?,?,?,?,?) ON CONFLICT(caller) DO UPDATE SET job_json=excluded.job_json,targets=excluded.targets,op=excluded.op,titles=excluded.titles`, job.Actor, data, names, []byte(op), labels)
	return err
}
func (s *annotationService) updateTagJob(job *tagJob) error {
	data, err := json.Marshal(s.jobs.snapshot(job.Actor))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.rt.ctx), annotateIndexTimeout)
	defer cancel()
	_, err = s.rt.annotationReads().db.ExecContext(ctx, `UPDATE annotation_tag_job SET job_json=? WHERE caller=? AND json_extract(job_json,'$.id')=?`, data, job.Actor, job.ID)
	return err
}
func (s *annotationService) restoreTagJobs() {
	store := s.rt.annotationReads()
	if store == nil || s.rt.ctx == nil {
		return
	}
	rows, err := store.db.QueryContext(s.rt.ctx, `SELECT job_json,targets,op,titles FROM annotation_tag_job`)
	if err != nil {
		s.logf("annotations: restore jobs: %v", err)
		return
	}
	type saved struct {
		job     tagJob
		targets []string
		op      json.RawMessage
		titles  map[string]string
	}
	var jobs []saved
	for rows.Next() {
		var data, names, labels []byte
		var item saved
		if err = rows.Scan(&data, &names, &item.op, &labels); err != nil {
			break
		}
		if err = json.Unmarshal(data, &item.job); err != nil {
			break
		}
		if err = json.Unmarshal(names, &item.targets); err != nil {
			break
		}
		if err = json.Unmarshal(labels, &item.titles); err != nil {
			break
		}
		jobs = append(jobs, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		s.logf("annotations: restore jobs: %v", err)
		return
	}
	for _, item := range jobs {
		item := item
		resume := item.job.State == tagJobRunning || item.job.State == tagJobInterrupted
		if resume {
			item.job.State = tagJobRunning
		}
		if s.jobs.start(&item.job) && resume {
			s.background(func() { s.runTagJob(&item.job, item.targets, item.op, item.titles) })
		}
	}
}
