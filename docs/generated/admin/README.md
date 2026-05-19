# Cassini admin docs

## Start here

- First overview: [Start here](./start-here.md)
- Bring up the stack: [Quick start](./quick-start.md)
- Runtime model: [System overview](./system-overview.md)

## Suggested reading order

1. [Start here](./start-here.md)
2. [Quick start](./quick-start.md)
3. [System overview](./system-overview.md)
4. [Deployment stack](./deployment-stack.md)
5. [Operator runtime](./operator-runtime.md)
6. [Storage and promotion](./storage-and-promotion.md)
7. [Day-2 operations](./day-2-operations.md)
8. [Reference](./reference/README.md)

## Fast paths

### I want to bring the stack up locally

Read:

1. [Quick start](./quick-start.md)
2. [Deployment stack](./deployment-stack.md)
3. [Configuration](./reference/configuration.md)

### I want to understand jobs, attempts, and reruns

Read:

1. [System overview](./system-overview.md)
2. [Operator runtime](./operator-runtime.md)
3. [Operator API](./reference/api.md)

### I want to understand storage and live-site behavior

Read:

1. [Storage and promotion](./storage-and-promotion.md)
2. [Deployment stack](./deployment-stack.md)
3. [Storage and filesystem reference](./reference/storage-and-filesystem.md)

### I am troubleshooting an existing stack

Read:

1. [Day-2 operations](./day-2-operations.md)
2. [Troubleshooting](./reference/troubleshooting.md)
3. [Operator API](./reference/api.md)

## Cassini in one paragraph

Cassini runs a file-driven meeting pipeline behind an operator service. The operator records, builds, and publishes through the Cassini CLI; the control panel operates that runtime through the operator API; the viewer serves the resulting static meeting site from shared storage. The key operational boundaries are durable artifacts, preserved attempt history, and safe live-site promotion.
