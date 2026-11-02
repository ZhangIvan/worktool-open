#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""调用接线垫片（非实现）：主流程以 cwd=server/ 调用 `python test/e2e/run_cross_e2e.py`，
相对路径被解析为 server/test/e2e/run_cross_e2e.py。唯一事实源 = 仓库根
test/e2e/run_cross_e2e.py，本文件仅以 runpy 委托执行并透传退出码。
（E2E 车道纪律：除本一处接线外，全部产出仅写 test/e2e/**；理由见 LaneResult 报告。）"""
import runpy
import sys
from pathlib import Path

# 垫片位于 server/test/e2e/（比真实实现的 test/e2e/ 深一层）：parents[3] = 仓库根
REAL = Path(__file__).resolve().parents[3] / "test" / "e2e" / "run_cross_e2e.py"
if not REAL.exists():
    print(f"[env] 未找到真实实现: {REAL}", file=sys.stderr)
    sys.exit(2)
sys.argv[0] = str(REAL)
runpy.run_path(str(REAL), run_name="__main__")
