package common

// HeavyCapBinding reports that the OPERATOR'S CAP — not the money, not the absence of a yard — is
// what bars the next heavy. It reads HeavyReserve's own inputs over HeavyReserve's own cap rung, so
// the two cannot disagree about what "at the cap" means. It authorises and withholds nothing.
//
// WHY IT IS NEEDED BESIDE THE RESERVATION: HeavyReserve collapses every "cannot" into one answer,
// 0, and downstream a zero meaning "the operator capped us" and one meaning "we have never priced a
// yard" call for opposite responses. The wave's reason cannot separate them either (sp-suzfh), so
// anything acting on the cap reads it as a fact rather than inferring it from a label.
//
// The PRICE is not consulted: at the cap the pricing errand correctly stops reading yards, so
// requiring an ask would make this unsatisfiable in the state it names. CapabilityOpen IS, because
// with no known heavy yard the class is barred by availability and calling that "the cap" lies.
func HeavyCapBinding(in HeavyReserveInputs) bool {
	return in.CapabilityOpen && in.HeavyCap > 0 && in.HeaviesOwned >= in.HeavyCap
}
