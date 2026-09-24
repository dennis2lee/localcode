#!/usr/bin/env bash
# Shared MSI checks for package-msi.sh. Sourced, not executed.
#
# rep_order_ok reads an InstallExecuteSequence export (msiinfo format) on
# stdin. It succeeds when RemoveExistingProducts is sequenced after
# InstallExecute and before InstallFinalize, which is WiX's
# afterInstallExecute: the old product is removed after the new product's
# files land and inside the install transaction, so a failed or cancelled
# install rolls back to the old version instead of to nothing.
rep_order_ok() {
	awk -F'\t' '
		$1 == "InstallExecute" { ie = $3 }
		$1 == "RemoveExistingProducts" { rep = $3 }
		$1 == "InstallFinalize" { fin = $3 }
		END { exit !(ie != "" && rep != "" && fin != "" && ie + 0 < rep + 0 && rep + 0 < fin + 0) }
	'
}

# component_guid_ok reads a Component table export (msiinfo format) on
# stdin. It succeeds when each of the six components below carries its
# pinned GUID, name and GUID together on one row.
#
# Late removal is what makes this load-bearing rather than cosmetic.
# With RemoveExistingProducts after the new files land, the old product
# is removed while the new one is already there, and a file survives
# that removal only where both products share the component. wixl
# derives Guid='*' from the key path, so renaming a component (or its
# key file) silently mints a new GUID: the new product's files stop
# being shared, and the old product's removal deletes them. A GUID that
# changed while the path stayed the same breaks every upgrade the same
# way. The six values are the ones in the released 0.144.0 through
# 0.148.0 MSIs, and in a scratch MSI built from this tree's .wxs.
component_guid_ok() {
	awk -F'\t' '
		BEGIN {
			want["StartMenuShortcut"] = "{CDF15D13-14FE-5F06-A3B2-012643C4633D}"
			want["GuiStartMenuShortcut"] = "{C0F9469E-71CE-5663-B250-FA1C24A1C0E2}"
			want["DesktopShortcut"] = "{814874AF-2341-553B-AD0A-2C00868F69F3}"
			want["MainExecutable"] = "{A510A015-C768-501F-8987-92D5158A8BCE}"
			want["GuiExecutable"] = "{C696DA9E-2106-5D3F-99C2-FDEAC9E24E91}"
			want["WebView2BootstrapperFile"] = "{83486582-9C79-5D23-8243-4BEA93C359B5}"
		}
		($1 in want) {
			found[$1] = 1
			if ($2 != want[$1]) {
				printf "component %s has GUID %s, want %s\n", $1, $2, want[$1]
				bad = 1
			}
		}
		END {
			for (n in want) {
				if (!(n in found)) {
					printf "component %s is missing\n", n
					bad = 1
				}
			}
			exit (bad ? 1 : 0)
		}
	'
}
