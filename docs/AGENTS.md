# xlpeek 使用说明（面向 agent）

你有一个名为 `xlpeek` 的命令行工具，用于读取和分析电子表格文件
（XLSX / XLSM / XLTM / XLTX / XLAM）。
它是**只读**的：只能读，不能写、不能改格式、不能生成图表。

> **本文档是权威版本。** 若需要一份只有约 500 token 的精简版常驻系统提示词，
> 见 [SYSTEM_PROMPT.md](SYSTEM_PROMPT.md) —— 那是本文的压缩副本，细节以本文为准。

---

## 一、先记住这些（其余的可以现查）

1. **永远先跑 `info`。** 不要猜 sheet 名，不要假设表头在第 1 行。
2. **汇总用 `agg`，不要自己翻页加总。** 把 2700 行拉进上下文只为了算一个数，是最大的浪费。
3. **不熟悉的数据，先 `profile` 再 `agg`。** 有些列的值根本不可比（见第五节），不先查会得出错误结论而不自知。
4. **页大小用 500–5000。** 小页的开销主要花在进程启动上，29 次小请求比 1 次大请求慢 15 倍。
5. **每次调用只输出一个 JSON 信封到 stdout**，退出码 `0` 成功 / `1` 运行时错误 / `2` 用法错误。
6. **同一个文件要连问多个问题时，用长连接（见 `serve`）。** 独立调用每次都要重新解析文件；
   长连接只解析一次，实测快约 30 倍。如果你的运行环境只能一次一条命令，就忽略这条。

---

## 二、输出契约

每次调用往 stdout 写且只写一个 JSON：

```json
{"ok":true,"command":"read","version":"1.0.3","data":{ ... }}
{"ok":false,"command":"read","version":"1.0.3","error":{"code":"SHEET_NOT_FOUND","message":"..."}}
```

- **错误也在 stdout**，所以只有一个解析路径。先看 `ok`。
- 输出是 **UTF-8**。若你用本地代码页解码，非 ASCII 会乱码。
- 默认紧凑单行 JSON（省 token）。人类可读用 `--pretty`。
- flag 放在文件路径前后都可以。取值写作 `--limit 10` 或 `--limit=10`；
  布尔 flag 三种写法都行：`--ignore-case`、`--ignore-case=true`、`--ignore-case true`。
- **退出码 2 = 改参数就能修**（列名不存在、表达式非法、sheet 不存在）；
  **1 = 改参数没用**（文件不存在、不是工作簿、读取失败）。据此决定是重试还是换个问法。

### ⚠️ 上下文预算：每次响应都会报出自身大小

信封里有 `data_bytes`：

```json
{"ok":true,...,"data_bytes":919262,"data":{...}}
```

超过 512 KB 时额外给一句 `data_warning`：

```json
"data_warning":"this response is 919262 bytes; narrow it with --columns, a smaller --limit, or --format tsv"
```

**看到 `data_warning` 就要立刻收窄查询**，不要继续翻页。

**形状有上限，总量没有。** 行数最多 5000、列数最多 128，但两者相乘没有封顶：

| 查询 | 实测输出 |
|---|---|
| `read -l 5000`（13 列宽表） | 919 KB ≈ **29 万 token** |
| 同上但 `--format tsv` | 398 KB ≈ 12 万 token |
| `read -l 5000 --columns a,b,c` | 288 KB |
| `profile` 全表 | 3.7 KB |
| `agg` 按区域分组（11 组） | 1.7 KB |

理论上限：5000 行 × 128 列的中文单元格 ≈ **11 MB ≈ 360 万 token**。**单次调用足以撑爆上下文。**

**真正的危险是累积**：单次 3 万 token 看着不多，翻 20 页就是 60 万——每次都不炸，二十次之后炸了。

省 token 的手段，按效果排序：

1. **`--columns` 投影**——最大杠杆
2. **`--format tsv`**——再省约 2.3 倍
3. **宽表把 `--limit` 开小**（500~1000，别用 5000）
4. **要汇总就用 `agg`**，不要读行再加总

### 错误码与应对

| code | 含义 | 你应该做什么 |
|---|---|---|
| `SHEET_NOT_FOUND` | sheet 名不对（也可能是索引越界） | **消息里已列出所有可用 sheet**，直接用正确的名字或索引重试 |
| `COLUMN_NOT_FOUND` | 列名不对 | 消息里说明了期望格式。改用列字母或序号，或先跑 `profile` 看真实列名 |
| `FILE_NOT_FOUND` | 路径不对 | 检查路径。Windows 路径注意分隔符 |
| `PASSWORD_REQUIRED` | 文件加密了，但你没给密码 | 向用户索要密码后加 `--password` 重试 |
| `INVALID_PASSWORD` | 密码给错了 | 向用户确认正确密码，**不要反复重试** |
| `UNSUPPORTED_FORMAT` | 不是有效工作簿（如 PNG、VBA 工程文件、损坏文件） | 确认文件真的是 xlsx；不要反复重试 |
| `USAGE` | 参数写错 | 读 `message`，改正参数。不要原样重试 |
| `READ_ERROR` | 其他读取失败 | 可能损坏、超限或路径过长 |

---

## 三、六个命令

### `info <file>` — 这个工作簿里有什么

**第一步总是这个。** 默认很便宜（只读维度和少量预览，不扫全表）。

```bash
xlpeek info book.xlsx
xlpeek info book.xlsx --deep              # 走全表，给精确行数
xlpeek info book.xlsx -s 明细 --sample 5  # 只看某张表，多看几行
```

返回每张表的：`name`、`visible`、`dimension`、`max_row`/`max_column`、`header`（表头行）、
`header_keys`（`--header` 模式下会产生的 JSON 键）、`column_names`（A/B/C…）、`sample_rows`。
`--deep` 追加 `last_row`、`populated_rows`、`last_populated_row`、`merged_ranges`。

> **要判断「数据到第几行结束」，看 `last_populated_row`。**
> `max_row` 是文件自己的说法，会把「只有格式没有内容」的空行算进去（拖一个边框到第 200 行，
> 它就报 200）；`last_row` 是扫描停在哪；`populated_rows` 是**计数**不是位置。
> `last_populated_row` 才是最后一个有内容的行号，用它算分页边界。

> `dimension` / `max_row` / `max_column` 来自文件自带的维度元素，**只是提示，可能不准**。
> 真正可信的是 `header`、`column_names` 和实际读出来的行。

**`-s` 也接受 sheet 序号**（从 0 开始，和 `info` 报的 `index`、`read` 报的 `sheet_index` 一致）。
名字优先于序号，所以有张表真叫 `1` 时仍按名字选：`xlpeek read book.xlsx -s 0`。

### `read <file>` — 分页读取

```bash
xlpeek read book.xlsx -s 明细 -l 3                    # 前三行，数组形式
xlpeek read book.xlsx -s 明细 --header -l 100          # 行转对象
xlpeek read book.xlsx -s 明细 --header -o 100 -l 100   # 下一页
xlpeek read book.xlsx -s 明细 --header-row 3 --header  # 表头在第 3 行
xlpeek read book.xlsx -s 明细 --header --columns "订单号,金额"
xlpeek read book.xlsx -s 明细 --header --where "金额>10000" -l 50
xlpeek read book.xlsx -s 明细 --header --skip-empty    # 跳过空行
xlpeek read book.xlsx -s 明细 --header --fill-merged   # 合并单元格填值
xlpeek read book.xlsx -s 明细 --header --format tsv -l 1000   # 最省 token
xlpeek read book.xlsx -s 明细 --header --dates iso -l 1000    # 日期形态固定
xlpeek read book.xlsx -s 明细 --header --columns "A:D"        # 列区间
```

分页元数据是自驱动的：

```json
{"offset":0, "limit":100, "rows_returned":100,
 "first_row":2, "last_row":101,
 "has_more":true, "next_offset":100, "rows_scanned":100}
```

**翻页规则：拿到 `rows` → 若 `has_more` 为 true → 用 `next_offset` 作为下次的 `--offset` → 重复。**

关键语义：

- **`--offset` 数的是"返回的行"，不是电子表格的行号。** 有 `--where` 时数的是**命中的行**。
  物理行号由 `first_row`/`last_row` 单独给出，可以映射回单元格坐标（`first_row` 是绝对行号）。
- **先看 `complete` 字段。** `complete: true` 表示你手上的就是全部；`false` 表示还
  有更多行。**不要**把 `complete: false` 的一页当成整张表来下结论。`find` 同理
  （对应 `truncated`）。
- **`complete` 看的是内容，不是位置。** 表格末尾「只有格式没有内容」的空行不算数据，
  所以最后一页会直接给 `complete: true`，不会让你翻一堆空白页。表格中间的空洞不受影响，
  预读照样会跨过去。要更干净就用 `--skip-empty`。
- **默认不假设表头。** `--header` 等价于 `--header-row 1`。
- `--header` 模式下，空表头单元格用列字母代替，重复表头加 `_2` 后缀——**列名永远唯一**。
- 行会**补齐到统一宽度**，页与页之间不会变形。`columns` 给出本页的列字母。

### `agg <file>` — 直接拿分析结果（重要）

**当用户问的是"总共多少""按 X 分组""排前几""占比"这类问题时，用这个，不要翻页加总。**

```bash
# 按区域分组：合计、计数、比率
xlpeek agg book.xlsx -s 明细 --header --group-by 销售区域 \
    --sum 收入金额 --sum 成本金额 --count \
    --derive "毛利率=(sum_收入金额-sum_成本金额)/sum_收入金额"

# 不带 --group-by 就是总计
xlpeek agg book.xlsx -s 明细 --header --sum 收入金额 --count

# 前 10 名
xlpeek agg book.xlsx -s 明细 --header --group-by 客户名称 \
    --sum 收入金额 --sort-by sum_收入金额 --limit 10

# 对表达式求和（不只是对列）
xlpeek agg book.xlsx -s 明细 --header --sum "净利=收入金额-成本金额"

# 先过滤再汇总
xlpeek agg book.xlsx -s 明细 --header --where "销售区域=华北" --sum 收入金额

# 跨行合并的标签列 / 无缓存值的公式列
xlpeek agg book.xlsx -s 明细 --header --group-by 销售区域 --sum 收入金额 --fill-merged
xlpeek agg book.xlsx -s 明细 --header --sum 收入金额 --calc
```

聚合函数：`--sum` `--avg` `--min` `--max` `--count` `--count-distinct`，
每个都可用多次，都接受算术表达式（`+ - * /` 和括号），都可以用 `名字=表达式` 重命名。

> **列名本身含运算符、括号或空格时，写成方括号**：`--sum "[金额(万元)]"`。
> 中文报表里 `金额(万元)`、`收入(USD)` 这类列名很常见，不加方括号会被当成算术式而报错
> ——报错信息里也会给出这个写法。

`--derive "名字=表达式"` 基于**已算出的聚合字段**再算指标——比率必须这样做：
`sum(收入-成本)/sum(收入)` 才是整体毛利率，逐行毛利率求平均是错的。
引用不存在的字段会直接报 `USAGE` 错，不会静默返回 null。

输出字段命名分两种，都可预测：

| 写法 | 字段名 |
|---|---|
| `--sum 收入金额`（不命名） | `sum_收入金额` —— **加前缀** |
| `--sum "收入=[金额(万元)]"`（自己命名） | `收入` —— **就是你给的名字，不加前缀** |

`count` 固定叫 `count`。写了 `name=` 就别再按文档里的前缀去引用它——
`--derive` 引用不存在的字段会报 `USAGE`，并且会告诉你正确的名字是什么。

方括号列名产出的字段名也带方括号，`--derive` 里照抄即可：

```bash
xlpeek agg book.xlsx -s 明细 --header --sum "[金额(万元)]" --sum "[成本(万元)]" --derive "毛利率=(sum_[金额(万元)]-sum_[成本(万元)])/sum_[金额(万元)]"
```

**语义要点：**
- **空单元格被跳过，不当 0 处理。** 所以均值不会被空值拉低。但表达式内部的空值按电子表格惯例算 0。
- 一组若没有任何可用值，返回 `null` 而不是误导性的 `0`。
- 求和用了补偿算法，结果按 15 位有效数字输出，与 Excel 精度一致。
- `--sort-by` 对聚合字段是**降序**，对分组键是升序。`--limit`/`--offset` 对**输出分组**分页。
- 无法解析为数字的单元格会被跳过。**跳过多少很重要**，所以警告里同时给出跳过数和总数
  （跳过 3 个和跳过 597 个是完全不同的信号），通过 `warning_count` 判断有没有发生。
- **带数字格式的列按显示值参与计算**：`¥1,234.50`、`12.35%`（= 0.1235）都能直接求和，
  默认模式与 `--raw` 得到同一个数。要在完整存储精度上算，仍用 `--raw`。
- `--calc` 与 `--fill-merged` 的含义和 `read` 完全相同（求无缓存公式 / 把合并区值填进所跨单元格），
  两者都会让 excelize 载入整张表，因此都是显式 opt-in。
- **三条"数字对、前提错"的告警**（都在 `warnings` 里，都会让 `warning_count > 0`）：
  引用的列**一个可用数值都没有**（聚合结果是 `null`；消息会区分"单元格是空的"——多半是
  无缓存公式，提示 `--calc`——还是"有值但都是文本"，后者不要加 `--calc`）；
  同一列**混用了多种货币**（每个单元格都能解析，总和依然没有意义）；
  有行**分组键为空**（多半是合并标签列，消息里会提示 `--fill-merged`）。

**⚠️ `unaccounted_columns` —— 务必检查这个字段。**

工具会自动检查：**有哪些列既没参与分组、也没参与聚合，却在被汇总的行里存在多个取值**。
这些列是你汇总结果里"数字对、含义可能错"的根源。

```json
"unaccounted_columns": [
  {"column":"单位","distinct":2,"sample":["元","千元"],"suspected":true},
  {"column":"结算方式","distinct":4,"sample":["信用证","银行承兑汇票","电汇","商业承兑汇票"]}
],
"warning_count": 1,
"warnings": ["column \"单位\" holds 2 distinct values (元, 千元) but takes no part in ..."]
```

- `suspected: true` 表示列名看起来像单位/币种/口径（`单位`、`币种`、`汇率`、`unit`、`currency`、`rate`…）。
- **只要 `warning_count > 0`，在把数字报给用户之前必须先把可疑列加进 `--group-by` 看一眼。**
- 基数过高的列（如凭证号）不会出现在这里——它们几乎肯定是标识符而非维度。

**`complete` 字段**：`true` 表示这就是全部分组；`false` 表示还有更多，用 `next_offset` 继续。

**⚠️ 高基数分组会退化成"倒表"。** 按近似唯一的列分组（凭证号、客户编号）会返回一行一组
——那不是汇总，是换了形式的全表：

```bash
--group-by 销售区域     → 11 组     1.8 KB   正常
--group-by 客户名称     → 65 组     7.2 KB   正常
--group-by 凭证号       → 2700 组   181 KB   ⚠️ 这就是全表
```

`--limit` 默认 **1000**：超过就只回前 1000 组，并给 `has_more` / `next_offset` 让你接着取，
同时在 `warnings` 里说明这是默认上限截的（不是你设的）。分组数超过 1000 时工具一定会出声。
**要"前 N 名"就用 `--sort-by <字段> --limit N`**，不要先拉全量再自己截；
真要全部就用 `--limit 0` 明说。

### `serve` — 长连接模式（同一文件问很多问题时用）

每次调用都要重新打开并解析文件。若你要对**同一个文件连续问多个问题**，用 `serve` 打开一次即可。

```bash
xlpeek serve
{"command":"info","args":["book.xlsx","-s","明细"]}
{"ok":true,...}
{"command":"agg","args":["book.xlsx","-s","明细","--header","--sum","收入金额"]}
{"ok":true,...}
{"command":"quit"}
```

- **每行一个 JSON 请求，每行一个 JSON 响应**，一一对应（失败的请求也占一行，会话不中断）。
- `args` 就是你平时的那套参数，**没有任何新语法要学**。
- 实测 20 次查询：独立进程 265ms/次 → serve 2ms/次（缓存命中），**快约 30 倍**。
- 附带好处：不用每次重复写 `-s 表 --header --header-row 3`，少了一处写错就改变语义的地方。

`serve` 自身的参数：

| 参数 | 作用 |
|---|---|
| `--cache N` | 同时保持打开的工作簿数，默认 4。默认值足够在多个文件间轮转而不重复解析（实测缓存命中 0ms vs 颠簸 6ms） |
| `--idle-timeout 5m` | 空闲这么久后自行退出；默认 0 表示不超时 |

`{"command":"ping"}` 可随时探测会话状态：

```json
{"ok":true,"command":"ping","data":{"status":"ok","uptime_seconds":12,
 "requests":3,"cached_workbooks":["a.xlsx","b.xlsx"]}}
```

**结束会话**：发 `{"command":"quit"}`（会收到应答），或直接关闭 stdin（EOF）。空闲超时退出**不产生额外输出行**——这是刻意的，否则你下次读到的"响应"会是这条无关消息。

### `profile <file>` — 每一列到底是什么

**对不熟悉的数据，在 `agg` 之前跑这个。** 它会告诉你哪些列的值不可比。

```bash
xlpeek profile book.xlsx -s 明细 --header
xlpeek profile book.xlsx -s 明细 --header --columns 销售区域,币种
xlpeek profile book.xlsx -s 明细 --header --calc          # 无缓存公式列也能看出来
xlpeek profile book.xlsx -s 明细 --header --fill-merged   # 合并标签列不再算作空
```

逐列返回：`type`（`number`/`date`/`text`/`empty`/`mixed`）、填充率、`distinct` 去重数、
数值列的 `min`/`max`/`mean`、以及基数低时的 `top_values`（取值 + 计数）。

> **类型看的是单元格呈现出来的值**：`¥1,234.50`、`12.35%` 都算 `number`——数字格式只是
> 存储值外面的一层装饰，不是另一种数据。但**带公式却没有缓存值的列**（openpyxl / pandas /
> xlsxwriter 生成的表就是这样）默认会显示成 `empty`，加 `--calc` 才能正确归类；
> **合并标签列**的填充率默认是失真的，加 `--fill-merged` 才是真实覆盖率。

`top_values` 不存在时，看 `values_omitted` 区分原因，**不要假定"没列出 = 没有值"**：

| `values_omitted` | 含义 |
|---|---|
| `cardinality` | 取值太多（超过 `--max-values`），故未列举 |
| `empty` | 该列全为空，确实没有值 |
| `disabled` | 你传了 `--max-values 0` 主动关闭了枚举 |

> **`type` 只有在该列所有非空值都一致时才会给出具体类型**，否则是 `mixed`。
> 看到 `mixed` 要警惕——意味着有些行无法按数字处理。
>
> 去重统计有上限（单列 2 万、总计 50 万）。超限时 `distinct_capped: true`，
> 此时 `distinct` 是**下界**而非精确值。

### `find <file>` — 这个值在哪

```bash
xlpeek find book.xlsx -s 明细 -v "张三"
xlpeek find book.xlsx -s 明细 -v "^A-\d+$" --regex
xlpeek find book.xlsx -s 明细 -v "已取消" --header --column 状态 -l 20
```

返回每条命中的单元格坐标、行列号、所在列名、以及**整行上下文**。

`--offset` 数的是**命中数**（不是行数），配合 `next_offset` 续页，语义与 `read` 一致：

```bash
xlpeek find book.xlsx -s 明细 -v "已取消" --header -l 20 -o 20
```

`truncated: true` 表示**确实还有**下一条命中——工具会多找一条再下结论，所以最后一页是
`truncated: false` / `complete: true`，不用靠"再翻一页看看"来确认。
`truncated_approximate: true` 表示预读 1 万行仍没找到下一条就放弃了，此时是"可能有"。

---

## 四、过滤语法（`--where`）

可重复，多个条件之间是 **AND**，在聚合之前生效。

| 运算符 | 含义 |
|---|---|
| `=` `!=` | 相等 / 不等，**大小写不敏感** |
| `>` `>=` `<` `<=` | 比较 |
| `~` | 子串包含，大小写不敏感 |

列可以写成**表头名**（`--header` 模式下）、**列字母**（`B`）或 **1-based 序号**（`2`）。

```bash
--where "销售区域=华北" --where "收入金额>2000000"
--where "客户名称~香港"
```

> **一个条件只能有一个运算符。** 第一个运算符之后的内容全是值：`金额>>100`、
> `金额>`、`金额>100>200` 一律报 `USAGE`（退出码 2），不再"尽力解释"。
> 理由：以前这几条会成功返回行数——`金额>` 甚至匹配**全表**（空值比任何东西都大）
> ——调用方看不出任何异常。
>
> 值里**真的**含 `> < = ~` 时用引号：`--where "备注~'a>b'"`。

> 过滤值能解析成数字时走**数值比较**，此时**无法解析为数字的单元格直接判为不匹配**，
> 而不会退化成文本比较。这个行为是有意的——避免"看起来有结果但其实是字符串比较"。
>
> **反过来要小心**：过滤值**不是**数字、而单元格是数字时，比较只能按文本做（日期区间
> 靠的就是这个），于是 `--where "金额>1OO"`（数字 `0` 敲成字母 `O`）会得到一组
> **看起来正常、其实完全无关**的行。这种情况现在一定告警：
> `warning_count > 0`，且 `warnings` 里点明过滤值、列名、命中了多少个数值单元格。
> **看到这个告警就说明这个过滤条件问的不是你想问的问题**，改用 `~` 或改对值。
>
> **"能解析成数字"包含数字格式带来的修饰**：千分位逗号、两侧的货币符号、以及结尾的
> `%`。`%` 是**比例**（`20%` 就是 `0.2`），所以 `--where "比率>20%"` 不会命中显示为
> `5.00%` 的单元格，阈值收紧到 `2%` 结果只会变少。
>
> **货币符号只当作装饰，不换算**：因此默认模式和 `--raw` 给出相同的过滤结果；同一列
> 混用 `¥`/`$` 时按数值大小比较（这种混合由 `agg` 负责报出来）。
>
> 完全不是数字的值（`A-1`、`2024-08-21`）仍然按文本比较——日期区间过滤靠的就是这个。
> **要在完整存储精度上比较，仍用 `--raw`**：默认模式下比较的是显示值。
>
> **等值比较按 15 位有效数字**：`25.67%` 换算成 `0.2567` 时可能差一个 ulp，
> 所以 `--raw --where "比率=25.67%"` 也能命中存储值为 `0.2567` 的单元格。
> 范围比较（`>` `<`）本来就不受影响。

---

## 五、会让答案悄悄出错的陷阱

**这一节是本文档最重要的部分。** 这些不是报错，是"看起来成功了但结论是错的"。

### 陷阱 1：汇总了不可比的值（最常见、最严重）

一个 `单位` 列可能混着「元」和「千元」；一个 `币种` 列可能混着 CNY / USD / HKD。
**直接 `--sum 收入金额` 会得到毫无意义的数字。**

实例：某文件 naive 相加得 84.5 亿，统一单位与汇率后是 120.5 亿，**低估 29.9%**。

> **好消息：`agg` 现在会自动检测这种前提条件破坏**，并在 `warnings` + `unaccounted_columns`
> 里报出来（见上面 `agg` 一节）。工具不可能知道「千元 ≠ 元」，但它能看见「这一列有多个
> 取值，而你直接加总了」。**看到 `warning_count > 0` 就必须先处理，再报数字给用户。**

**做法：**

```bash
# 1. 先用 profile 看有没有可疑的维度列
xlpeek profile book.xlsx -s 明细 --header

# 2. 有单位/币种列时，把它们一起 group by，自己组合
xlpeek agg book.xlsx -s 明细 --header --group-by 单位,币种 --sum 收入金额 --count
```

拿到分组结果后，按业务规则折算再相加。**永远不要对可能混合单位的列直接求和。**

同理要警惕：`分部`、`状态`、`版本` 这类维度列里是否存在语义不同的取值。

### 陷阱 2：合并单元格看起来是空的

报表常用合并单元格做跨行/跨列标签。电子表格只在**左上角**存值，其余为空。
不处理的话你会看到"这一行大部分是空的"，然后据此推理。

```bash
xlpeek info book.xlsx --deep          # 看 merged_ranges 是否 > 0
xlpeek read book.xlsx -s 明细 --fill-merged
xlpeek agg book.xlsx -s 明细 --header --group-by 销售区域 --sum 收入金额 --fill-merged
xlpeek profile book.xlsx -s 明细 --header --fill-merged
```

`read` / `agg` / `profile` 都支持这个 flag。`agg` 另外会在**有行落进空分组**时主动告警
（提示 `--fill-merged`），所以即使不传也不会静默失真。

### 陷阱 3：表头不在第 1 行

真实报表常见「标题行 + 空行 + 表头」。直接用 `--header` 会把标题当成列名。

```bash
xlpeek info book.xlsx --header-row 3 --sample 2   # 先确认表头位置
xlpeek read book.xlsx -s 明细 --header-row 3
```

### 陷阱 4：稀疏表格的空行

读取是**按位置**的：从第 1 行到该表最后一行，每一行都会占一个位置。
数据从第 19 行开始的表，前 18 行都会返回空行；底部若有一个残留的带格式空行，
每页都会被撑到百万行。

```bash
xlpeek read book.xlsx -s 明细 --header --skip-empty
```

### 陷阱 5：默认精度是 15 位有效数字

默认返回值按 Excel 的 15 位有效数字规范化。存储值 `677675.5699999999` 默认返回 `677675.57`。
**做进一步计算或需要与原始文件核对时，用 `--raw`** 拿到精确存储值。

### 陷阱 6：截断了却以为读完了

**`complete` 是显式否定，比 `has_more` 更难被忽略。** `complete: false` 意味着你手上的
不是全部——不要基于它下"总计""都是"这类结论。

三个截断信号，全部要检查：

| 字段 | 出现位置 | 含义 |
|---|---|---|
| `complete: false` | `read` / `agg` / `find` | 还有更多，用 `next_offset` 继续 |
| `truncated: true` | `find` | 达到 `--limit` 或 `--max-scan` 而停止（确实还有下一条） |
| `has_more_approximate: true` | `read` | 预读超过 1 万行仍无法判断，`has_more` 是保守估计 |
| `truncated_approximate: true` | `find` | 同上，`truncated` 是保守估计 |

`has_more_approximate` 的偏保守是刻意的：错报 `false` 会让你漏数据，错报 `true` 只多一次
请求。看到它时多翻一页确认，不要直接断定没有更多。

同理，`columns_capped: true`（`info`）和 `distinct_capped: true`（`profile`）分别表示
**列被截断**和**去重数只是下界**。

### 陷阱 7：程序生成的表格里，公式列看起来是空的

Excel 自己保存时会把公式的计算结果一并写进文件；openpyxl / pandas / xlsxwriter 生成的
表**不写**。于是这些表的公式列读出来是空串，`agg --sum` 得到 `null`——而单元格里明明有公式。

```bash
xlpeek read book.xlsx -s 明细 --header --calc               # 求值后读出
xlpeek agg book.xlsx -s 明细 --header --sum 收入金额 --calc
xlpeek profile book.xlsx -s 明细 --header --calc
```

求值会让 excelize 载入整张表，所以默认关闭。但 `agg` 在"引用的列一个可用数值都没有"
时会主动告警并提示 `--calc`，不会只回一个 `null` 让你去猜。

### 陷阱 8：过滤值写错一个字符，问的就不是原来的问题

`0` 与 `O`、`l` 与 `1` 这种误敲，会让过滤值不再是数字；此时比较只能按**文本**做，
于是 `--where "金额>1OO"` 命中的是"按字符比大小"的那几行——**行数看起来很正常**，
没有任何线索表明过滤没按数值生效。

```bash
xlpeek read book.xlsx -s 明细 --header --where "金额>1OO" -l 9
# → ok:true，但 warning_count > 0，warnings 里点名 "1OO" 不是数字、多少个数值单元格被按文本比较
```

**`warning_count > 0` 就说明这个过滤条件问的不是你想问的问题**：要么改对值，
要么本来就该用 `~` 做子串搜索。

同理，`--where` 的表达式必须**恰好一个运算符**：`金额>>100`、`金额>`、`金额>100>200`
现在一律报 `USAGE`，不再静默返回一个看似正常的行数。

### 陷阱 9：日期形态随文件而变

日期存的是数字、显示的是数字格式，所以同一个值在这个文件里是 `2026-01-01 0:00:00`、
在那个文件里是 `01-01-26`，而 `--raw` 给的是 `46023`。`01-01-26` 对下游是**有歧义**的
（月日顺序、两位年份），跨文件比较或排序之前先固定形态：

```bash
xlpeek read book.xlsx -s 明细 --header --dates iso -l 1000
```

`--dates iso` 给 `2026-01-01`（存了时刻则给 `2026-01-01T09:30:00`），
`--dates serial` 给存储的数字。默认 `display` 保持文件原样——因为判断"哪些单元格是日期"
必须读数字格式，那会载入整张表，所以和 `--calc` 一样是显式 opt-in。

---

## 六、推荐工作流

### A. 探索一个陌生工作簿

```bash
xlpeek info book.xlsx                     # 有哪些表、表头长什么样、几行
xlpeek info book.xlsx --deep              # 精确行数、是否有合并单元格
xlpeek profile book.xlsx -s 目标表 --header   # 每列是什么、有什么取值
```

做完这三步再决定怎么读。**不要跳过 `profile`。**

### B. 回答一个分析问题

```bash
# 1. 用 agg 直接问，而不是拉数据自己算
xlpeek agg book.xlsx -s 明细 --header --group-by 销售区域 --sum 收入金额 --count

# 2. 需要细分时，把可疑维度一起分组
xlpeek agg book.xlsx -s 明细 --header --group-by 销售区域,单位,币种 --sum 收入金额 --count

# 3. 排序取前几名
xlpeek agg book.xlsx -s 明细 --header --group-by 客户名称 --sum 收入金额 \
    --sort-by sum_收入金额 --limit 10
```

### C. 定位并读取特定记录

```bash
xlpeek find book.xlsx -s 明细 -v "KH-10043" --header --column 客户编号
# 拿到行号后，用 offset 定向读取附近的数据
xlpeek read book.xlsx -s 明细 --header -o 41 -l 5
```

### D. 导出大量数据

```bash
xlpeek read book.xlsx -s 明细 --header --format tsv -l 5000
```
TSV 模式第一行是 JSON 信封（含分页元数据），之后是表头行 + 制表符分隔的数据。
比 JSON 对象模式省约 56% 的体积。字段内的制表符/引号/换行按 CSV 规则加引号。

---

## 七、成本模型：不要浪费上下文

| 做法 | 代价 |
|---|---|
| 单次调用启动 + 打开文件 | ~0.09 秒（固定开销） |
| 扫描 2900 行 | ~0.11 秒（约 38 微秒/行） |
| 翻 2900 行 @ `--limit 100`（29 次调用） | 4.57 秒 |
| 翻 2900 行 @ `--limit 2900`（1 次调用） | 0.30 秒 |

**主要成本是每次调用的启动，不是扫描。** 所以：

- **页大小用 500–5000。** 小页是纯浪费。
- **不要为了加总而翻页** —— 用 `agg`。返回 11 行 vs 2700 行，是数量级的差别。
- **不要重复读同一页。** 记住 `next_offset`。
- **能用 `--columns` 投影就投影**，减少传输体积。
- **`--format tsv`** 在需要大量行时比 JSON 省一半以上体积。
- 深 offset 每次都要从头扫描。**优先用 `--where` 缩小范围，而不是翻到很深的页码。**
- **`--calc` 与 `--fill-merged` 会放弃流式**：它们让 excelize 把整张表载入内存，
  大表上是实打实的额外开销，只在确有必要时加。`info --deep` 则是实打实的全表扫描，
  行数很大时明显变慢（它不会把表载入内存，但要把每一行走完）。

---

## 八、限制：什么时候该告诉用户你做不到

- **写操作**：改单元格、改格式、生成图表、做数据透视表 —— 全部不支持。
- **公式依赖分析**：`--calc` 只能求值，不能告诉你公式引用了哪些单元格。
- **超大文件的精确统计**：`--deep` 和 `profile` 都要扫全表。行数极大时会明显变慢。
- **加密文件**：报 `PASSWORD_REQUIRED` 时说明文件有密码，**只有用户能给你**——向用户索要，
  不要试图绕过，也不要反复重试。拿到后加 `--password` 重试。
- **未跑过的平台**：`bin/` 里有 windows/amd64 和 linux 的 amd64/arm64。
  确认目标平台匹配——**exe 不能在 Linux 上跑**。

---

## 九、速查卡

```bash
xlpeek info    <f> [--deep] [-s 表] [--header-row N] [--sample N]
xlpeek read    <f> [-s 表] [--header] [--header-row N] [-o N] [-l N]
                      [--columns a,b|A:D] [--where expr] [--skip-empty]
                      [--fill-merged] [--calc] [--raw] [--dates iso|serial]
                      [--format json|tsv|csv|markdown]
xlpeek agg     <f> [-s 表] [--header] [--group-by a,b]
                      [--sum e] [--avg e] [--min e] [--max e] [--count]
                      [--count-distinct c] [--derive "n=e"] [--where expr]
                      [--sort-by 字段] [--limit N] [--offset N]
                      [--calc] [--fill-merged]
xlpeek profile <f> [-s 表] [--header] [--columns a,b|A:D] [--max-values N] [--where expr]
                      [--calc] [--fill-merged]
xlpeek find    <f> [-s 表] -v 值 [--regex] [--ignore-case] [--column c]
                      [--header] [--header-row N] [-l N] [-o N] [--max-scan N]
                      [--dates iso|serial]
xlpeek serve       [--cache N] [--idle-timeout 5m]
xlpeek ping

通用：[-s 表名或序号] [--raw] [--password P] [--tmpdir D] [--pretty]

约定值：read 的 --limit 默认 100、上限 5000；find 的 --limit 默认 50；
        agg 的 --limit 默认 1000（0 = 不限）；--max-columns 默认 128；
        profile 的 --max-values 默认 20。
        超出范围会直接报 USAGE 错，不会静默截断。

退出码：0 成功；2 = 改参数就能修（列名/sheet 不存在、表达式非法）；
        1 = 改参数没用（文件不存在、不是工作簿、读取失败）。

必查字段：complete（是否完整）、warning_count（必须为 0 才能直接报数字）、
         warnings（> 0 时**逐条读**：同一列混用多种货币、有行落进空分组、
         引用的列没有任何可用数值、过滤值不是数字却被按文本比较——
         这四类都是"结果可能不是你要的"的信号，先按消息里的提示处理再下结论）、
         unaccounted_columns（汇总时是否有未计入的可疑维度列）、
         data_bytes / data_warning（响应多大，是否该收窄查询）
```
