#!/usr/bin/env bash
# Tests build/msi-checks.sh against canned InstallExecuteSequence dumps.
# Run: build/msi-checks-test.sh. Exit nonzero on the first failure.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$ROOT/msi-checks.sh"

pass=0
fail=0
check() { # name, want(0/1), dump...
	local name="$1" want="$2"
	shift 2
	if printf '%s' "$1" | rep_order_ok; then got=0; else got=1; fi
	if [ "$got" = "$want" ]; then
		pass=$((pass + 1))
	else
		fail=$((fail + 1))
		echo "FAIL: $name (want exit $want, got $got)"
	fi
}

LATE='Action	Condition	Sequence
InstallExecuteSequence	Action
PublishProduct		6400
InstallExecute		6500
RemoveExistingProducts		6550
InstallFinalize		6600
'
EARLY='Action	Condition	Sequence
InstallExecuteSequence	Action
InstallValidate		1400
RemoveExistingProducts		1401
InstallInitialize		1500
InstallFinalize		6600
'
NOEXECUTE='Action	Condition	Sequence
InstallExecuteSequence	Action
RemoveExistingProducts		6550
InstallFinalize		6600
'

check "late removal passes" 0 "$LATE"
check "early removal fails" 1 "$EARLY"
check "missing InstallExecute fails" 1 "$NOEXECUTE"

check_guid() { # name, want(0/1), dump...
	local name="$1" want="$2"
	shift 2
	if printf '%s' "$1" | component_guid_ok; then got=0; else got=1; fi
	if [ "$got" = "$want" ]; then
		pass=$((pass + 1))
	else
		fail=$((fail + 1))
		echo "FAIL: $name (want exit $want, got $got)"
	fi
}

GUIDS_OK='Component	ComponentId	Directory_	Attributes	Condition	KeyPath
s72	S38	s72	i2	S255	S72
Component	Component
StartMenuShortcut	{CDF15D13-14FE-5F06-A3B2-012643C4633D}	AppMenuFolder	260		reg1973A20DB9091B505D476F859475DE73
GuiStartMenuShortcut	{C0F9469E-71CE-5663-B250-FA1C24A1C0E2}	AppMenuFolder	260		regFFDBB3F5DD3A4D6DD45D8F43A683641F
DesktopShortcut	{814874AF-2341-553B-AD0A-2C00868F69F3}	DesktopFolder	260		regC06DE2BED0FBBEB305B6F1ACF12B4F70
MainExecutable	{A510A015-C768-501F-8987-92D5158A8BCE}	INSTALLDIR	256		LocalCodeEXE
GuiExecutable	{C696DA9E-2106-5D3F-99C2-FDEAC9E24E91}	INSTALLDIR	256		LocalCodeGuiEXE
WebView2BootstrapperFile	{83486582-9C79-5D23-8243-4BEA93C359B5}	INSTALLDIR	256		WebView2BootstrapperEXE
'
GUID_CHANGED='Component	ComponentId	Directory_	Attributes	Condition	KeyPath
s72	S38	s72	i2	S255	S72
Component	Component
StartMenuShortcut	{CDF15D13-14FE-5F06-A3B2-012643C4633D}	AppMenuFolder	260		reg1973A20DB9091B505D476F859475DE73
GuiStartMenuShortcut	{C0F9469E-71CE-5663-B250-FA1C24A1C0E2}	AppMenuFolder	260		regFFDBB3F5DD3A4D6DD45D8F43A683641F
DesktopShortcut	{814874AF-2341-553B-AD0A-2C00868F69F3}	DesktopFolder	260		regC06DE2BED0FBBEB305B6F1ACF12B4F70
MainExecutable	{00000000-0000-0000-0000-000000000000}	INSTALLDIR	256		LocalCodeEXE
GuiExecutable	{C696DA9E-2106-5D3F-99C2-FDEAC9E24E91}	INSTALLDIR	256		LocalCodeGuiEXE
WebView2BootstrapperFile	{83486582-9C79-5D23-8243-4BEA93C359B5}	INSTALLDIR	256		WebView2BootstrapperEXE
'
GUID_MISSING='Component	ComponentId	Directory_	Attributes	Condition	KeyPath
s72	S38	s72	i2	S255	S72
Component	Component
StartMenuShortcut	{CDF15D13-14FE-5F06-A3B2-012643C4633D}	AppMenuFolder	260		reg1973A20DB9091B505D476F859475DE73
GuiStartMenuShortcut	{C0F9469E-71CE-5663-B250-FA1C24A1C0E2}	AppMenuFolder	260		regFFDBB3F5DD3A4D6DD45D8F43A683641F
MainExecutable	{A510A015-C768-501F-8987-92D5158A8BCE}	INSTALLDIR	256		LocalCodeEXE
GuiExecutable	{C696DA9E-2106-5D3F-99C2-FDEAC9E24E91}	INSTALLDIR	256		LocalCodeGuiEXE
WebView2BootstrapperFile	{83486582-9C79-5D23-8243-4BEA93C359B5}	INSTALLDIR	256		WebView2BootstrapperEXE
'

check_guid "pinned GUIDs pass" 0 "$GUIDS_OK"
check_guid "a changed GUID fails" 1 "$GUID_CHANGED"
check_guid "a missing component fails" 1 "$GUID_MISSING"

echo "pass=$pass fail=$fail"
[ "$fail" = 0 ]
