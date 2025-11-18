#!/bin/bash

#================================================================
#   
#   
#   文件名称：start.sh
#   创 建 者：肖飞
#   创建日期：2025年11月18日 星期二 09时46分19秒
#   修改日期：2025年11月18日 星期二 15时18分56秒
#   描    述：
#
#================================================================
function main() {
	virtualenv -p /usr/bin/python3.10 venv
	. venv/bin/activate
	pip install -r requirements.txt
	python app.py
}

main $@
