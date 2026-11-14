package normalizer

import (
	"regexp"
	"strings"
)

// Role families answer "is this a job for someone who writes software?". A job gets
// at most one; "" means anything else (sales, HR, finance, support, design, ...).
const (
	RoleSoftware = "software"       // backend, frontend, full stack, mobile, embedded, generic engineer
	RoleAIData   = "ai_data"        // machine learning, AI, data science, data engineering, analytics
	RoleInfra    = "infra_security" // DevOps, SRE, cloud, platform, network, security
	RoleQA       = "qa"             // QA, test automation, SDET
)

var (
	// Someone who does technical work. Topic words ("AI/ML") alone do not count: a
	// "Senior Product Manager - AI/ML" is not an AI engineer.
	techNounRe = regexp.MustCompile(`\b(engineer|developer|scientist|researcher|architect|programmer|analyst|sde|swe)\b`)
	roleNounRe = regexp.MustCompile(`\b(engineer|developer|scientist|researcher|architect|programmer)\b`)

	// Roles that are clearly not engineering. They veto everything below unless the title
	// also names a technical role ("Data Analyst, Marketing" stays a data job).
	nonEngRoleRe = regexp.MustCompile(`\b(product (manager|management|marketing|owner|designer|lead)|program manager|project manager|recruit(er|ing|ment)?|talent|sales|marketing|account (executive|manager)|customer success|legal|counsel|finance|accountant|payroll|human resources|hr|technician)\b`)

	// "Developer Advocate" and "Sales Engineer" use the words but do not build software.
	devRelRe     = regexp.MustCompile(`\bdeveloper (advocate|relations|evangelist|marketing|community)\b|\bdevrel\b`)
	nonTechEngRe = regexp.MustCompile(`\b(sales|solutions?|support|customer|field|pre-?sales|post-?sales|technical support|consulting|manufacturing|mechanical|electrical|civil|process|industrial|chemical|facilities|hardware|audio|acoustic|optical|implementation|professional services|success) (engineer|architect|consultant|scientist)\b`)

	// Roles that are a job on their own, whatever surrounds them.
	aiRoleRe = regexp.MustCompile(`\b(data scientist|data science|data engineer|data engineering|data analyst|data architect|applied scientist|research scientist|ml engineer|ai engineer|mlops|analytics engineer|business intelligence|bi developer|bi engineer|bie|applied science|research science)\b`)
	// Topics that make an engineer an ML/AI engineer. The weak ones lose to "software".
	aiStrongTopicRe = regexp.MustCompile(`\b(machine learning|deep learning|nlp|computer vision|llm|generative ai|genai|ml)\b`)
	aiWeakTopicRe   = regexp.MustCompile(`\b(ai|ai/ml)\b`)

	qaRe = regexp.MustCompile(`\b(qa|qae|quality assurance|sdet|test engineer|test automation|automation engineer|automation tester|software tester|quality engineer)\b`)

	infraRe = regexp.MustCompile(`\b(devops|devsecops|sre|site reliability|reliability engineer|reliability engineering|platform engineer|infrastructure engineer|infrastructure software|infrastructure architect|cloud engineer|cloud architect|cloud security|security engineer|security architect|security researcher|security analyst|appsec|application security|product security|cyber ?security|information security|infosec|network engineer|systems? engineer|systems? administrator|database administrator|dba|kubernetes|production engineer|observability|vulnerability (management )?engineer|penetration test(er|ing)|cloud operations engineer|monitoring engineer)\b`)

	softwareRe = regexp.MustCompile(`\b(software|developer|sde|swe|programmer|backend|back-end|back end|frontend|front-end|front end|full[- ]?stack|web engineer|mobile engineer|android|ios|embedded|firmware|member of technical staff|mts|forward deployed engineer(ing)?|engineering (manager|director|lead|head)|(manager|director|head|vp|vice president),? (of )?(software )?engineering|vp,? engineering|technical lead|tech lead)\b`)

	// A bare "Engineer" says little. Departments here are team names ("Cloud Data Path",
	// "App Modernization ENG"), so they cannot prove a job is technical; they can only show
	// it is not, as with "Principal Engineer" in Sales.
	genericRe     = regexp.MustCompile(`\b(engineer|architect|scientist|technologist|programmer)\b`)
	deptNonTechRe = regexp.MustCompile(`\b(sales|support|customer|success|solutions?|consulting|professional services|marketing|finance|legal|people|hr|recruiting|talent|operations|enablement|account|partner|channel|information technology|it)\b`)
)

// RoleFamily classifies a job from its title and department. It is deliberately
// conservative: a title that is not clearly a technical role gets "" instead of a guess.
func RoleFamily(title, department string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	d := strings.ToLower(strings.TrimSpace(department))
	if t == "" || devRelRe.MatchString(t) {
		return ""
	}
	if nonEngRoleRe.MatchString(t) && !techNounRe.MatchString(t) {
		return ""
	}
	customerFacing := nonTechEngRe.MatchString(t)
	software := softwareRe.MatchString(t)

	switch {
	case aiRoleRe.MatchString(t):
		return RoleAIData
	case aiStrongTopicRe.MatchString(t) && roleNounRe.MatchString(t) && !customerFacing:
		return RoleAIData
	case aiWeakTopicRe.MatchString(t) && roleNounRe.MatchString(t) && !customerFacing && !software:
		return RoleAIData
	case qaRe.MatchString(t):
		return RoleQA
	case infraRe.MatchString(t):
		return RoleInfra
	case software:
		return RoleSoftware
	}
	if genericRe.MatchString(t) && !customerFacing && !deptNonTechRe.MatchString(d) {
		return RoleSoftware
	}
	return ""
}
