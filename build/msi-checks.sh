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
