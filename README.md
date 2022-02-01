# Promtum

Promtun is a tiny tool to establish a tunnel between a Prometheus server and Coroot cloud.

![](./schema.svg)

## Run

### Docker

```bash
docker run --detach --name coroot-promtun \
    -e PROMETHEUS_ADDRESS=<INTERNAL_PROMETHEUS_HOST_AND_PORT> \
    -e PROJECT_TOKEN=<COROOT_PROJECT_TOKEN> \
    ghcr.io/coroot/promtun
```

### Kubernetes

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: promtun
  namespace: coroot
spec:
  selector:
    matchLabels: {app: promtun}
  replicas: 1
  template:
    metadata:
      labels: {app: promtun}
    spec:
      containers:
        - name: promtun
          image: ghcr.io/coroot/promtun
          env:
            - name: PROMETHEUS_ADDRESS
              value: <INTERNAL_PROMETHEUS_HOST_AND_PORT>
            - name: PROJECT_TOKEN
              value: <COROOT_PROJECT_TOKEN>
```
