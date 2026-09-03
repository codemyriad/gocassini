type SelectedJobIdentity = {
  job: {
    id: string;
  };
};

export function shouldShowDetailLoading(
  loadingDetail: boolean,
  selectedJob: SelectedJobIdentity | null,
  selectedJobId: string,
): boolean {
  return loadingDetail && (!selectedJob || selectedJob.job.id !== selectedJobId);
}
