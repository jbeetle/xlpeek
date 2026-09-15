# xlpeek 回归套件

一组可重复运行的检查，覆盖 Go 单元测试难以触及的行为：**与独立实现交叉验证**、
**真实文件上的鲁棒性**、以及**文档中的命令是否真能执行**。

```bash
python run_all.py              # 全部，约 75 秒
python run_all.py --verbose    # 同时打印每套的完整输出
python regress_agg.py          # 单跑某套
```

从任何目录运行都可以——`_common.py` 会把工作目录切到仓库根目录。

**退出码是唯一可靠的信号**：0 全通过，非 0 有失败。每套失败时会打印失败条目。

## 各套件

| 脚本 | 验证什么 | 为什么需要它 |
|---|---|---|
| `regress_agg.py` | 聚合结果与**独立 XML 解析**逐区域比对（33 项） | 工具自己算自己不算验证；两条独立路径一致才有说服力 |
| `regress_crosscheck.py` | CLI 与独立解析器**逐单元格**比对 FY24 全表 | 最早、最根本的一致性检查 |
| `regress_filter.py` | 9 种过滤表达式与全量复算比对，含 offset 语义与列投影 | 过滤错了会静默给出错误行 |
| `regress_paging.py` | 4 种页大小遍历全表结果必须一致；页边界无缝衔接 | 分页丢行或重复行是最难发现的缺陷 |
| `regress_new.py` | 合并单元格填充、`--header-row`、profile 统计量、TSV 版式 | 这些都与独立解析结果对账 |
| `regress_precision.py` | 每一处与原始 XML 的数值差异都能被「15 位有效数字规范化」解释 | 区分"精度显示差异"与"读取错误" |
| `verify_docs.py` | 抽取 AGENTS.md / README.md 中所有 bash 命令**实际执行** | 文档漂移会让 agent 照着做然后开始猜 |
| `regress_small.py` | 10 个小文件 × 7 个命令：零 panic、信封合法、数组字段非 null；含加密与损坏文件 | 首要不变量是"任何输入都不崩" |
| `serve_mem.py` | 300 次请求内存收敛，切换文件不累积 | 验证 `serve` 可以长期挂着 |

Node.js 封装有自己的测试（`../nodejs/test.js`，33 项），`run_all.py` 会一并运行。

## 夹具

`fixtures/header_row.xlsx` 是随套件一起提交的：一份标题块在表头上方的报表
（第 1-2 行是标题与单位，第 3 行才是表头，第 4-6 行是数据），用于验证 `--header-row`。
用 excelize 生成，形状即上述内容，需要重建时按这个结构写即可。

其余夹具来自仓库的 `test/` 目录。

## 指定二进制

`run_all.py` 与所有套件都尊重 `XLPEEK` 环境变量：

```bash
XLPEEK=/path/to/xlpeek python run_all.py    # 测一个特定构建
```

不设置时，按平台选择 `bin/xlpeek.exe` 或 `bin/xlpeek-linux-<arch>`。

想确认失败能被正确上报，可以把 `XLPEEK` 指向一个不存在的路径——
所有套件都应当失败，`run_all.py` 应当以非 0 退出。

## 换台机器要改什么

**不需要改任何东西。** 路径与二进制选择都由 `_common.py` 处理。唯一的外部依赖是
Python 3.7+（用到了 `sys.stdout.reconfigure`）与仓库里的 `test/` 夹具。

---

## 修订记录

这组脚本原本散落在临时目录里，整理时修掉了三个真问题：

1. **4 套失败时仍返回 exit 0**（`regress_small`、`verify_docs`、`regress_crosscheck`、
   `regress_precision`）——它们把失败**打印出来**但没有退出码，自动化会把失败当通过。
   现在统一走 `_common.finish()`。
2. **`XLPEEK` 对 Python 套件无效**——各脚本用的是自己硬编码的 `EXE`，
   只有 `run_all.py` 在读共享的那个。现在全部统一。
3. **`regress_new.py` 依赖 `C:\temp\hdr_test.xlsx`**——一个只存在于开发机上的临时文件，
   别人克隆仓库后该套件必然失败。现在夹具随套件提交。
