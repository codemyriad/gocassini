We're working on V1 slice of the MVP effort. Please read the artefacts in:
- planning/initiatives/mvp/*
- planning/initiatives/mvp/slices/V1/

Read the shaping skill and let's start talking about the solution. The next step should be to address the spikes.

## Persistence

The job log should be stored in SQLite.

The job entry includes fields (propose the full model and extend if needed):
- timestamps (state / stage transitions)
- id
- artifact path
- stage
- state

## The process model

Both the API (server) and the worker live in the same process, with:
- the server (API, e.g. POST /jobs) listening in a non-blocking way
- when the job is created, it's placed in a queue
- the workers from the worker pool pick up and execute jobs
- the workers are goroutines (non blocking)
- we should be able to set a max number of workers (as env variable, default to 1)

The workers should be separated by step:
- record workers - this can just be a placeholder for now
- build workers - this is the meat of this impl (mentioned above)
- publish worker - single worker -- ensuring sequential publish

Setting max workers for record and build should be separate, e.g.:
- max recordings workers is set, say 4, the 5th one tries to join, it fails with busy error
- max build workers is set to, say 4, the 5th one is ready to be built - it waits in a queue

## API 

POST `/jobs?provider="<provider>"`:
    - payload: depends on the provider:
        - let's keep it generic:
            - url
    - flow:
        - validates request against a schema based on the provider
        - checks if availale slots:
            - if yes:
                - logs the job started
                - delegates the recording to a worker
                - updates active count
                - returns ok status code with job id (corresponding to DB entry)
            - if not, errors out with status busy and reason max workers exceeded
    - error cases: 
        - unknown provider
        - missing data
        - busy (too many active recordings)

GET `/jobs`:
    - lists all jobs

GET `/jobs/:id`
    - retrieves job

## Important note for V1 implementation

V1 doesn't include Nextcloud meeting capture (recording), but we need to implement the job execution e2e in V1. In order to achieve that:
- the recording worker won't record, it will use the fixture and create a dest artifact, as if it were recorded.
- the build shold run full flow
- the publish should run full flow
