// entity.go 集中 sync 實體名稱的別名解析，避免各處各自維護一份清單。
package sync

import "strings"

// entityAliases 是大小寫不敏感的 entity 名稱對照（含單數別名）。
var entityAliases = map[string]Entity{
	"contacts":     EntityContacts,
	"contact":      EntityContacts,
	"accounts":     EntityAccounts,
	"account":      EntityAccounts,
	"invoices":     EntityInvoices,
	"invoice":      EntityInvoices,
	"creditnotes":  EntityCreditNotes,
	"creditnote":   EntityCreditNotes,
	"credit_notes": EntityCreditNotes,
	"payments":     EntityPayments,
	"payment":      EntityPayments,
}

// ParseEntity 以大小寫不敏感、去頭尾空白的方式解析實體名稱；
// 未知值回 ("", false)。
func ParseEntity(raw string) (Entity, bool) {
	e, ok := entityAliases[strings.ToLower(strings.TrimSpace(raw))]
	return e, ok
}
