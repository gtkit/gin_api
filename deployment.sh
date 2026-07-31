#!/bin/bash
#
# deployment.sh —— 代码部署脚本：git 拉取最新代码 + 校验配置就位 + 重启服务。
# 用法：bash deployment.sh <branch> <RootPath>
#
# ───────────────────────────────────────────────────────────────────────────
# 【只改配置、不改代码时】不必跑本脚本（省得 git reset 等多余动作），直接：
#   1) 编辑配置        vim <RootPath>/config/env/env.yml
#   2) 重启生效        supervisorctl restart gin_api
#   3) 确认起来了      supervisorctl status gin_api       # 须为 RUNNING
#   4) 没起来看日志    tail -n 100 /data/supervisord/gin_api_server-err.log
#                     （多半是配置写错被 Validate 挡下、fail-fast 退出）
#
# 为什么改配置一定要重启：配置在进程启动时一次性读入并装配成运行实例
# （DB 连接池 / Redis / JWT / 监听端口等），进程不重启就一直用旧值。
# 注意 restart 是先停再起、有短暂不可用，生产请避开高峰或滚动重启。
# ───────────────────────────────────────────────────────────────────────────
# 【改了 supervisor 启动命令（command=）时】光 restart 不生效——supervisorctl restart
# 只是拿 supervisord 内存里的旧 command 重启进程，改过的 command= 根本没被读进去。
# 改完 command= 必须让 supervisord 重新读配置文件：
#   1) 重新读取配置    supervisorctl reread     # 检测到 conf 变更
#   2) 应用并重启      supervisorctl update     # 自动重启受影响的程序（无需再单独 restart）
#   3) 确认起来了      supervisorctl status gin_api        # 须为 RUNNING
# 不需要重启 supervisord 守护进程本身（systemctl restart supervisor 之类），reread+update 即可。
# ───────────────────────────────────────────────────────────────────────────
RootPath=$2
echo "ssh://git@gitlab.superjq.com:47383${PROJECT_PATH}"

echo ${RootPath}

www_user=www
www_group=www

www_user_id=$(id -u $www_user)
www_group_id=$(id -g $www_user)

if [ -n "$1" ]
then
    Branch=$1
else
    Branch=master
fi


## 第一次部署代码
if [ ! -d "${RootPath}" ];then
	 echo "ssh://git@gitlab.superjq.com:47383${PROJECT_PATH}"
   echo "RootPath:"$RootPath
   git clone -b ${Branch} ssh://git@gitlab.superjq.com:47383${PROJECT_PATH}.git $RootPath
fi

cd $RootPath
echo "RootPath:"$RootPath
## 拉取代码
git clean -f -d > /dev/null
git fetch origin
git reset --hard origin/${Branch}

## 如果 config/pem 文件夹不存在则创建

#if [ ! -d "${RootPath}/config/pem" ];then
#    chown -r ${www_user_id}.${www_group_id} ./
#    mkdir -p ${RootPath}/config/pem
#    echo "mkdir -p ${RootPath}/config/pem"
#fi


## 启动前校验外置配置就位（fail-fast：缺文件就别重启，免得服务起不来才发现）
## 配置统一为 config/env/env.yml（不进 git、被 .gitignore 忽略，故上面的 git clean 不会清掉它）；
## 由本机参照 config/env.yml.example 手动维护，运行环境由文件内的 env 字段决定。
CONFIG_FILE="${RootPath}/config/env/env.yml"
[ -f "$CONFIG_FILE" ] || { echo "缺少配置文件 $CONFIG_FILE（参照 config/env.yml.example 创建），中止部署"; exit 1; }

## 执行 migration —— CI 全自动迁移、无人工确认（团队定：test/master 推送即自动 migrate up）。
## 直接跑 git 分发的二进制（不走 make migrate-up：它非 dev 分支带 read 交互确认，CI 无 tty 用不了）；
## 迁哪个库由 config/env/env.yml 的 env 字段决定，故不带环境名。注意 MySQL DDL 不可事务回滚，误迁代价大。
## fail-closed：二进制缺失或迁移失败即中止部署、不重启，避免半迁移的库带病上线（up 只跑未执行的，会打印实际执行了哪些）。

[ -x ./bin/gin_api ] || { echo "缺少可执行二进制 ./bin/gin_api，中止部署"; exit 1; }

#echo "执行 migration（migrate up，自动、无确认）..."
#./bin/gin_api migrate up || { echo "迁移失败，中止部署，不重启服务。"; exit 1; }
#echo "迁移已完成。"


## 启动服务
echo "启动服务..."
#supervisorctl restart gin_api
echo "服务状态..."
#supervisorctl status

