package ra

type Capability string

const (
	CapabilityDepth             Capability = "depth"
	CapabilityMergeinfo         Capability = "mergeinfo"
	CapabilityLogRevprops       Capability = "log-revprops"
	CapabilityPartialReplay     Capability = "partial-replay"
	CapabilityCommitRevprops    Capability = "commit-revprops"
	CapabilityAtomicRevprops    Capability = "atomic-revprops"
	CapabilityInheritedProps    Capability = "inherited-props"
	CapabilityEphemeralTxnprops Capability = "ephemeral-txnprops"
	CapabilityFileRevsReverse   Capability = "file-revs-reverse"
	CapabilityList              Capability = "list"
	CapabilitySvndiff1          Capability = "svndiff1"
	CapabilitySvndiff2          Capability = "svndiff2"
)
