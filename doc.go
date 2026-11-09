// Package netops is the root of a domain kernel for network operations
// tooling: typed evidence, rule-based assessment, configuration drift and
// controlled change execution.
//
// The module is vendor neutral. Device syntax, domain models and rule
// catalogs belong to consumer modules such as go-vxlan and go-wireless.
//
//	evidence  observation status, source references, coverage, fingerprints
//	assess    severity, findings, rules, catalogs, policy and the engine
//	drift     deviations from baseline, peer consensus or a previous snapshot
//	change    typed changes, immutable plans and plan envelopes
//	apply     the single-plan execution state machine and its journal
//	store     private durable report artifacts and execution checkpoints
//	session   stable failure codes for connector errors
//	textscan  cached patterns and label tables for command output parsers
package netops
