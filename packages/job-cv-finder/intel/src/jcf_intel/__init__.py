"""Job CV Finder intelligence worker.

Python owns intelligence, not state: every function here is a pure transformation from a
task payload (job + Ruxt candidate snapshot) to a contract-shaped result. The Go core
validates and persists results and alone decides workflow transitions.
"""

ENGINE_NAME = "jcf-intel"
ENGINE_VERSION = "0.1.0"
