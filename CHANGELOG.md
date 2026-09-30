# Changelog

## Unreleased

### Added

- `Value.Detach()`：返回完全自持有（self-contained）的深拷贝。原有 `Parser` /
  `Scanner` / `Arena` / `ParserPool` / `ArenaPool` 的零分配复用语义不变；
  Detach 出来的值在后续 `Parser.Parse*`、`Scanner.Next`、`Arena.Reset` 或
  pool `Put` 之后仍然可以安全读写。对象键顺序、字符串的 raw escaped 形态与
  数字字面量均逐字节保留。

### 结构选择（为什么这样落地）

- 不新增一套并行数据结构。`Detach` 仍走仓库现有的 `Value` / `Object` / `kv`
  抽象：对象复制 `Object.kvs`，数组复制 `Value.a`，字符串/键通过
  `strings.Clone` 复制底层字节，标量分配新的 `Value`。与 `MarshalTo` /
  `parseValue` 使用完全相同的类型分支（`TypeObject`/`TypeArray`/
  `typeRawString`/`TypeString`/`TypeNumber`/`TypeTrue`/`TypeFalse`/`TypeNull`），
  未知类型沿用 `MarshalTo` 的 `panic(fmt.Errorf("BUG: ..."))` 风格，不引入
  新的错误模型。
- 复制的是 raw（escaped）键与字符串，而不是 unescape 后再拷贝。这样：
  1) 不触发 `Object.unescapeKeys()`，原对象的 `keysUnescaped` 状态与键字符串
     不被修改；2) Detach 值的 `MarshalTo` 与原值逐字节一致；3) Detach 值自身
     后续调用 `Get` 时仍按既有链路 unescape，写入的是自己的拷贝，不回连源缓冲。
- 即使 `null/true/false` 在 JSON 语义上是不可变单例，也为每个出现位置分配新的
  `Value`，保证“脱离树与原树之间不共享任何可变或不可变 `Value` 指针”这一更严格
  的不变量，且后续对数组槽位做 `Set` 时天然互不影响。
- 稀疏数组的 `null` 空洞（`SetArrayItem` 越界补位产生）逐槽递归拷贝，长度与空洞
  位置保持一致。
- nil receiver 返回 nil，与仓库其余 `Value`/`Object` 方法的 nil-safe 约定一致。

### 原覆盖空白（本次补上的测试缺口）

旧用例只覆盖“同一个生命周期内”的 parse / set / del / reset（如
`TestArena`、`TestObjectDelSet`、`TestValueDelSet`），没有任何用例覆盖
“值需要活过 Parser/Arena/pool 复用”的场景；此前唯一安全手段是
`MarshalTo` 后重新 `Parse`。新增 `detach_test.go` 覆盖这一空白。

### 最危险的反例与对应用例（用例 → 分支 → 不变量）

- **Parser 复用后字符串/数字缓冲被覆盖**：`TestValueDetachHugeNumber`
  解析超大数字字面量后 Detach，再连续 `Parse` 20 次，断言字面量逐字节不变；
  覆盖 `detachValue` 的 `TypeNumber`/`typeRawString` 拷贝分支。
- **连续 pool 复用**：`TestValueDetachParserPoolReuse` Detach 子树后
  `Put`，再用覆盖型 JSON 反复 `Get/Parse/Put` 20 轮，断言脱离子树内容与可变性；
  这是“同一批 `Value`/字节被重新分发”最直接的回归点。
- **Arena.Reset 与 Arena 字节缓冲复用**：`TestValueDetachArenaReset`
  用 `NewString`（含引号/换行转义）、`NewNumberFloat64`、稀疏数组构造后
  Detach、Reset，并继续 5 轮重新分配，断言内容不变；覆盖 `strings.Clone`
  与稀疏 `null` 分支。
- **raw escaped string/key 不回连源缓冲**：
  `TestValueDetachNested`（`raw\nesc\t\"v` 的读取与 raw marshal）与
  `TestValueDetachEscapedKeyIndependence`（含 `\n` 转义的键：只对脱离对象
  触发 unescape，断言原对象 `keysUnescaped` 未被翻转、键 backing 不同、
  两边序列化一致）。
- **嵌套对象 + 键顺序**：`TestValueDetachNested` 用 `Visit` 断言
  `z,a,b` 顺序，对应逐 `kv` 顺序复制分支。
- **稀疏数组**：`TestValueDetachSparseArray` 断言 `[42,null,"gap",null,null,null]`
  的长度/空洞、每个 `null` 槽位是不同 `Value` 且不等于包级单例，并在脱离侧
  回填空洞后原值不变。
- **null/bool 单例**：`TestValueDetachSingletons` 断言脱离值类型正确、
  指针既不等于源槽位也不等于 `valueNull/valueTrue/valueFalse`。
- **任一側 Set/Del 不影响另一侧**：`TestValueDetachMutationsIsolation`
  对对象/数组/标量路径双向 Set/Del，逐字节比较两边 JSON。
- **白盒结构隔离**：`TestValueDetachNodeIsolation` 递归收集两棵树的全部
  `Value` 指针断言无交集、无包级单例，断言字符串/数字/键 backing data
  pointer 不同、`kv` 与数组 backing slice 不同地址，并在脱离对象上扩容 16 个
  键验证不会踩坏原对象（防止浅拷贝切片导致的 append 覆盖）。
- **Scanner.Next 失效窗口**：`TestValueDetachScanner` 覆盖
  `sc.c.reset()` 复用链路。
- **根标量/空容器**：`TestValueDetachRootScalars` 覆盖 raw string、超长整数、
  指数数字、null/bool、`[]`、`{}` 作为根的情况；
  `TestValueDetachTypeStringBacking` 覆盖已被 `Type()` 翻转为 `TypeString`
  的值；`TestValueDetachNil` 覆盖 nil receiver。
- **Detach 自身的非破坏性**：`TestValueDetachDoesNotChangeOriginal`
  断言 Detach 后原 JSON 不变、原对象 `keysUnescaped` 不被翻转。

### 刻意保持不变的公开行为

- `Parser.Parse/ParseBytes`、`Scanner.Next`、`Arena` 构造与 `Reset`、
  `ParserPool`/`ArenaPool` 的有效性边界与文档一字未改；默认路径仍零额外分配。
- `Detach` 不返回 error：纯结构拷贝，不解析、不校验；非法类型的处理与
  `MarshalTo` 一致（BUG panic）。
- 对象键顺序、重复键、稀疏数组空洞、数字字面量（含 `NaN/Inf`、超 float64
  精度整数）、best-effort unescape 规则均保持与原链路一致。
- 原值在 Detach 后完全不变（包括 `keysUnescaped` 惰性标志）。

### 验证

- `go build ./...`
- 先跑定向用例：`go test -race -run TestValueDetach -count=10 .`
- 再跑全套：`go test -race -count=3 ./...`
