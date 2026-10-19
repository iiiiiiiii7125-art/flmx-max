# accepted-risks.md — 书面接受风险

C5 要求：P2 全部修复或有书面接受理由。本文件记录用户/审计接受的风险。

（R0 初始化，待填）

## R1（拟接受，待 R5 复核确认）

- SCR-016：docker-compose-v4/v6.yml 的 `POSTGRES_PASSWORD` 弱默认值 `flux_panel_change_me`。接受理由：install 流程（get_config_params→.env 生成）必写随机密码覆盖默认值；compose 未 publish 5432 端口；仅 `docker compose` 手动起且不走 install 的场景才暴露。残余风险：低。
