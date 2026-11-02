# LAB CLEARTEXT 候选：源码隔离与验证边界

状态：**候选源码；IMPLEMENTED_NOT_BUILT / NOT_DEVICE_TESTED**。没有部署、监听、代理、连接任何设备、安装APK、启用权限、ADB、CA变更或网络探测。没有下载SDK/Gradle/工具，没有push或触发CI。

## 最小变更

- 新 `lab` buildType继承debug的构建设置，但使用独立 `src/lab` 工厂、策略与manifest；applicationId增加 `.lab`，版本与应用标签显著标注 `LAB CLEARTEXT`。
- `src/main`只有 `EndpointPolicy` / `CommandPolicy` 抽象、普通HTTPS实现、通用route组装与策略调用；不含 `BuildConfig if LAB` 或RFC1918/明文实现。普通debug与release各自工厂只返回HTTPS策略；原main manifest仍 `usesCleartextTraffic=false`。
- 只有lab manifest覆盖 `usesCleartextTraffic=true`。这是一项**lab应用级Android开关**，不是系统级单IP防火墙；单origin约束由lab策略与唯一transport注入强制，APK隔离验收尚未执行，不能据源码声称二进制已验证。
- 原19个core JVM测试源码保持不变；新增lab表驱动/引擎边界8项、debug/release隔离各1项、公共配对/连接交错5项，共15个新增测试源码，均NOT_RUN。

## 每次连接的用户确认与边界

lab没有默认origin、不使用DNS或自动发现。用户须手输完整小写HTTP origin：RFC1918字面十进制IPv4与显式1–65535端口；无根路径斜杠。字段不恢复/不默认带出已保存地址。每次配对、每次恢复都弹出警告与精确origin，只有前台明确确认才进入bridge；持有配对身份不代表保留连接许可。

只接受RFC1918三段；代码使用整数边界，repo未写入任何实际运行设备地址。所有DNS、IPv6、整数/hex/octal主机、前导零、encoded字符、尾点、反斜杠、userinfo、路径、query、fragment、空白、隐式/非canonical端口都在URL库规范化之前拒绝。

一个内存策略实例同时传给配对HTTP、session HTTP和WebSocket握手；`AuthorizedRoutes`只允许两条固定API路径。HTTP与WS的请求origin都由该实例生成，不能另指定WS host。第二origin/端口在现有许可下拒绝。OkHttp禁普通/SSL重定向；任何3xx不视为成功。关闭前台页面、暂停、错误或断线清许可；恢复必须重新输入确认，已配对设备还需与受保护身份保存的origin相同。许可清除不撤销已经发出的包或已发生效果。

明文意味着网络上可以观察/篡改一次性实验配对与会话材料，**不是生产可用安全通道**。只能使用独立实验身份，不能填写真实业务凭据。此源码候选没有为这类网络风险增加TLS替代或通用隧道。

## 真正限制合成内容，而非只贴标签

lab `message.send_text`唯一目标是 `synthetic_target_001`；文本只允许精确 `LAB FIXTURE PING` 或 `LAB FIXTURE NONCE ` 后32位小写hex的固定模板。不得把真实数据塞进nonce。任意其他target/text在持久保存命令body之前关闭拒绝，不写入合成收件箱。相同策略也限制本应用FixtureActivity按钮，避免本页任意文本被当作合成输出。

`file.export`仍只有app内置 `synthetic_note_001/002` 的hash，必须既有服务批准scopeDigest和本地逐次选择吻合。没有路径/URI/文件选择/真实数据读取，没有新增远程enum。无障碍仍不注册、不启用，不宣称企微支持。

接收端限制不能阻止不可信服务先在明文网络上发送真实内容；后续任何实验服务仍必须采用同样固定fixture，并且需要单独授权，当前未部署。

## 已执行与待执行

实际已执行：`python android-client/tools/verify_variant_isolation.py`，只读取本候选源码，SOURCE ISOLATION PASS；`git diff --check -- android-client`无空白错误。两者不是编译或JVM测试。

待正式CI候选审核后才可执行的任务名（本轮NOT_RUN）：

- `:app:testDebugUnitTest`、`:app:testReleaseUnitTest`、`:app:testLabUnitTest`
- `:app:assembleDebug`、`:app:assembleRelease`、`:app:assembleLab`
- 产物隔离：`python tools/verify_variant_isolation.py --apkanalyzer <existing-official-sdk-tool> --apk debug=<apk> --apk release=<apk> --apk lab=<apk>`

隔离脚本先验证sourceSet工厂/manifest/路由注入与原19coretests，再在显式提供已有官方SDK工具及APK时读取manifest和DEX：普通debug/release不能有lab策略或fixture标记、必须禁止明文且无`.lab` applicationId；lab必须有`.lab`和明文标记。当前没有APK，因此 **APK ISOLATION NOT_RUN**。

下一步仅代码审阅与获准的正式CI候选构建；不得把本文件视为部署、安装、监听、运行设备或建立内网访问的授权。

## 候选复核后的取消修补与部署前缺项

ConnectionIntentGate在enroll/resume桥接入口捕获取消epoch，暂停/离页同步推进，worker启动与连接前核对，旧排队意图不能在暂停后重新授权。PairingAttemptGate另在公开enroll调用入队前捕获attempt，worker发包前核未失效，不能在worker里重新给旧请求分配许可。它也是回调接受的线性化点：暂停/离页会在排入worker前同步作废attempt；网络已收到但尚未被worker接受的响应不能持久化身份，重复回调也不能再次接受。transport保存并取消在途enrollment Call；取消失败/迟到响应仍经generation及gate复核，pairingInFlight总会清理。已在取消之前被gate接受的响应属于已开始的保存操作，暂停不能回溯撤销它；网络上已消费的enrollment也不能被取消倒退，失败需管理端给新实验身份，不自动重试旧token。新增5个纯交错JVM测试源码，当前NOT_RUN。

**部署前尚缺的硬门禁：** 本候选没有lab WS会话总寿命硬截止、累计任务/待处理队列总额限制；HTTP 30秒与hello 5秒不是WS总期限。64 KiB检查发生在OkHttp组装完整消息之后，不保证网络帧内存峰值≤64 KiB。后续须明确实现客户端/独立实验入口的硬截止与消息/队列预算、来源/路径ACL，并通过专门测试，才可谈部署。该实验入口当前未实现/未启动，不能假设外部proxy已提供保护。
