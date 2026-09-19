package portable

import (
	annotations "cassini-annotations"
	"regexp"
)

type Annotations = annotations.Annotations
type AnnotationTag = annotations.AnnotationTag
type AnnotationItem = annotations.AnnotationItem
type AnnotationTarget = annotations.AnnotationTarget
type AnnotationActor = annotations.AnnotationActor

var ParseAnnotations = annotations.ParseAnnotations
var ValidateAnnotations = annotations.ValidateAnnotations
var ValidateAnnotationLabel = annotations.ValidateAnnotationLabel
var NewAnnotationTagID = annotations.NewAnnotationTagID
var NewAnnotationItemID = annotations.NewAnnotationItemID
var NewAnnotationOperationID = annotations.NewAnnotationOperationID
var IsAnnotationID = annotations.IsAnnotationID
var IsAnnotationTagNamespace = annotations.IsAnnotationTagNamespace
var ValidateAnnotationTarget = annotations.ValidateAnnotationTarget

const AnnotationsFormatV1 = annotations.AnnotationsFormatV1
const AnnotationTargetMeeting = annotations.AnnotationTargetMeeting
const AnnotationTargetTimeRange = annotations.AnnotationTargetTimeRange
const AnnotationActorPerson = annotations.AnnotationActorPerson
const AnnotationActorAgent = annotations.AnnotationActorAgent
const MaxAnnotationTags = annotations.MaxAnnotationTags
const MaxAnnotationItems = annotations.MaxAnnotationItems
const MaxAnnotationLabelRunes = annotations.MaxAnnotationLabelRunes

var annotationIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var ErrAnnotationsFormatUnsupported = annotations.ErrAnnotationsFormatUnsupported
