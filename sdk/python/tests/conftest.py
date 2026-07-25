import os

# Never fire the anonymous install ping during the test suite (no real network,
# no marker-file churn). Individual tests can still exercise install_ping directly.
os.environ.setdefault("PYYOL_NO_TELEMETRY", "1")
