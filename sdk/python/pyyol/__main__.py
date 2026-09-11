"""So `python -m pyyol` is the same as the `pyyol` console script."""

from .cli import main

if __name__ == "__main__":
    raise SystemExit(main())
