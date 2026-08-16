# The benchmark agent image: one model, one game, one seat per container.
#
# Built from the REPO ROOT so the local SDK is in context:
#   docker build -f harness/bench/Dockerfile -t pyyol-bench:latest .
#
# The SDK is installed from source rather than from PyPI on purpose. The benchmark's whole
# claim is that the harness is the one developers actually get, so it has to be measured
# against the SDK in this working tree — a pinned release could drift from the movetools
# canonicaliser the gateway is running, and a canonicaliser mismatch shows up as honest
# moves being rejected mid-run.
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

WORKDIR /app

# SDK first, then the harness. Two layers so an edit to the harness — which is most edits —
# does not reinstall the SDK and its dependencies.
COPY sdk/python /app/sdk/python
RUN pip install --no-cache-dir /app/sdk/python

COPY harness/bench/pyproject.toml /app/harness/bench/pyproject.toml
COPY harness/bench/pyyolbench /app/harness/bench/pyyolbench
RUN pip install --no-cache-dir /app/harness/bench

# Shared across every container in a run: the budget file lives here, and so do the per-agent
# ledgers. Mounted as a volume by the runner; the default keeps a bare `docker run` working.
ENV PYYOLBENCH_STATE=/state
VOLUME ["/state"]

EXPOSE 9101

# Run as a non-root user. The container holds a live provider key in its environment and
# serves an HTTP port the platform calls; there is no reason for it to be root.
RUN useradd --create-home --uid 10001 bench && mkdir -p /state && chown -R bench /state /app
USER bench

# No CMD arguments: every knob is an environment variable, so the compose file or the run
# script is the complete definition of a run and nothing is hidden in an entrypoint.
CMD ["python", "-m", "pyyolbench.main"]
