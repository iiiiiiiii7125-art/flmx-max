# R16 FINAL 文档矩阵

基线：main @ b54b44f。

| 文档主张 | 证据 | 结论 |
|---|---|---|
| errors.md 码表与信封发射点一致（R15-X-01 关闭） | L1：`code: 500` 全仓发射点 = 0，三处 raw http.Error 逐一排除，八码逐码有发射点；L3 独立复核一致；check-doc-consistency.sh PASS | 一致 |
| demo 夹具含 backup/gist/status（R15-X-02 关闭） | L1：record_items.mjs:24 直接调用，两内容闸含 gist-status 键，走查 Run A exit 0；重录取证 80 键变、159/159 ts 全变 | 一致 |
| V-06 三处订正已入库 | L2：`git show b54b44f:` 直读，REA-UDIT 机理订正、routes-raw.csv idx 60/109 GET,HEAD、baseline.md :38 分布行均在位；diff 恰 9 文件 | 一致 |
| 文档一致性脚本全绿 | L2：check-doc-consistency.sh 在 b54b44f 树上重跑 exit 0，RESULT: PASS；字面量三处均为 1.4.8 | 一致 |
| R15-FIX-REPORT.md 已归档 | 协调者：原存 ~/.fixr15 未入库（9,164 字节，sha256 fda006d9…），已原样归档至 r15/；其 §3“仅此一处”结论作废 | 归档完成，结论作废 |
| 审计原文矛盾（R14-A13 :160、R14-A3 :62） | R15 V-05 已裁定正确机理以 R14 X-verdicts V-05 为准；本轮未复发 | 沿袭 |

本轮文档面新立案 0。

R16-FINAL-doc-matrix END
