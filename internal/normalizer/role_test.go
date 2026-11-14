package normalizer

import "testing"

func TestRoleFamily(t *testing.T) {
	cases := []struct {
		title, dept, want string
	}{
		// software
		{"Senior Software Engineer", "", RoleSoftware},
		{"Software Development Engineer II", "Software Development", RoleSoftware},
		{"SDE-2 (Backend)", "", RoleSoftware},
		{"Backend Engineer", "Engineering", RoleSoftware},
		{"Staff Backend Engineer, Payments", "", RoleSoftware},
		{"Frontend Developer", "", RoleSoftware},
		{"Full Stack Developer", "", RoleSoftware},
		{"Full-Stack Engineer", "", RoleSoftware},
		{"Android Engineer", "", RoleSoftware},
		{"iOS Developer", "", RoleSoftware},
		{"Member of Technical Staff", "", RoleSoftware},
		{"LMTS Agentic AP/Invoicing", "", RoleSoftware},
		{"SMTS Software Engineer", "", RoleSoftware},
		{"Technical Architect - Salesforce Devops", "", RoleInfra},
		{"Success Architect-Salesforce Field Service", "", ""},
		{"Engineering Manager, Platform", "", RoleSoftware},
		{"Forward Deployed Engineer", "", RoleSoftware},
		{"Manager, Forward Deployed Engineering", "", RoleSoftware},
		{"Embedded Software Engineer", "", RoleSoftware},
		{"Firmware Engineer", "", RoleSoftware},
		{"Staff Software Engineer, Security Platform: Authentication", "", RoleSoftware},
		{"Security Software Engineer", "", RoleSoftware},
		{"Intermediate Backend Engineer , Security Factory", "", RoleSoftware},
		// a bare "Engineer" is software unless the department shows it is something else.
		// Real department names are team names, so they cannot prove a job is technical.
		{"Staff Engineer", "Engineering", RoleSoftware},
		{"Staff Engineer", "App Modernization ENG", RoleSoftware},
		{"Staff Engineer", "Durability and Availability", RoleSoftware},
		{"Sr. Staff Engineer, Secure Web Gateway", "Cloud Data Path", RoleSoftware},
		{"Staff Engineer, DSPM", "Data Engineering", RoleSoftware},
		{"Principal Engineer", "", RoleSoftware},
		{"Senior Engineer II", "Technology", RoleSoftware},
		{"Principal Engineer", "Sales", ""},
		{"Principal Engineer", "Sales Engineering", ""},
		{"Staff Engineer", "Customer Success", ""},
		{"Engineer", "Information Technology", ""},
		{"Engineering Manager", "", RoleSoftware},
		{"Director of Engineering", "", RoleSoftware},
		{"VP, Engineering", "", RoleSoftware},
		{"Zscaler Sr. Software Development Engineer - Java/ Agentic AI/ Big Data", "", RoleSoftware},
		// AI, ML and data
		{"Machine Learning Engineer", "", RoleAIData},
		{"Senior Applied Scientist", "", RoleAIData},
		{"Data Scientist II", "", RoleAIData},
		{"Data Engineer", "", RoleAIData},
		{"AI Engineer", "", RoleAIData},
		{"Research Scientist, LLM Training", "", RoleAIData},
		{"MLOps Engineer", "", RoleAIData},
		{"Analytics Engineer", "", RoleAIData},
		{"Data Analyst", "", RoleAIData},
		{"Business Intelligence Developer", "", RoleAIData},
		{"BIE II, ShipTech Analytics", "", RoleAIData},
		{"Applied Science Manager, Advertising Trust", "", RoleAIData},
		{"Manager, Applied Science, Minerva", "", RoleAIData},
		{"Computer Vision Engineer", "", RoleAIData},
		{"Software Engineer, Machine Learning", "", RoleAIData},
		// infra, cloud and security
		{"DevOps Engineer", "", RoleInfra},
		{"Site Reliability Engineer", "", RoleInfra},
		{"Senior SRE", "", RoleInfra},
		{"Cloud Engineer", "", RoleInfra},
		{"Security Engineer", "", RoleInfra},
		{"Product Security Engineer", "", RoleInfra},
		{"Platform Engineer", "", RoleInfra},
		{"Network Engineer", "", RoleInfra},
		{"Systems Engineer", "", RoleInfra},
		{"Database Administrator", "", RoleInfra},
		{"Security Analyst", "", RoleInfra},
		{"Staff Vulnerability Management Engineer", "", RoleInfra},
		{"Cloud Operations Engineer", "", RoleInfra},
		{"Team Lead - Platform Monitoring Engineer", "", RoleInfra},
		// QA
		{"QA Engineer", "", RoleQA},
		{"SDET II", "", RoleQA},
		{"Test Automation Engineer", "", RoleQA},
		{"Quality Assurance Analyst", "", RoleQA},
		{"QAE II, Amazon Payments", "", RoleQA},

		// not a software job
		{"Senior Solutions Engineer – Pre-Sales", "Sales", ""},
		{"Sales Engineer", "", ""},
		{"Support Engineer", "Engineering", ""},
		{"Senior Support Engineer", "Support", ""},
		{"Customer Success Manager", "", ""},
		{"Technical Account Manager", "", ""},
		{"Product Manager", "", ""},
		{"Senior Product Designer", "Design", ""},
		{"Technical Program Manager", "", ""},
		{"Senior Technical Program Manager - Infrastructure", "", ""},
		{"Engineering Program Manager", "", ""},
		{"Senior Product Manager - AI/ML", "", ""},
		{"Product Manager, Machine Learning Platform", "", ""},
		{"Recruiter, Machine Learning", "", ""},
		{"Consulting Engineer - India", "", ""},
		{"Senior Technical Solutions Engineer (Platform)", "", ""},
		{"Solutions Architect", "", ""},
		{"HR Business Partner", "", ""},
		{"Senior Talent Partner ( Non-Tech Hiring)", "", ""},
		{"Manager / Senior Manager - Finance Tranformation", "", ""},
		{"Tax Analyst II, GST Tax Compliance", "", ""},
		{"Legal Counsel", "", ""},
		{"Marketing Manager", "", ""},
		{"Account Executive", "", ""},
		{"Customer Care Manager", "", ""},
		{"Payroll Industrial Trainee", "", ""},
		{"Lead, Quality", "", ""},
		{"Developer Advocate", "", ""},
		{"Developer Relations Manager", "", ""},
		{"Mechanical Engineer", "Operations", ""},
		{"Hardware Engineer", "", ""},
		{"Business Development Representative", "", ""},
		{"AI Operations Manager", "", ""},
		{"AI Product Manager", "", ""},
		{"AI Solutions Architect", "Sales", ""},
		{"Senior Manager, Security Operations", "", ""},
		{"Network Sales Engineer", "", ""},
		{"Fraud Operations Associate SDC", "", ""},
		{"Quality Assurance Technician, EasyShip", "", ""},
		{"Business Analyst II, Account Integrity", "", ""},
		{"Associate, ML Data Operations, GO-AI Operations", "", ""},
		{"Oracle Applications Functional Analyst, FinTech Cash Application", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := RoleFamily(c.title, c.dept); got != c.want {
			t.Errorf("RoleFamily(%q, %q) = %q, want %q", c.title, c.dept, got, c.want)
		}
	}
}

func TestNormalizeSetsRoleFamily(t *testing.T) {
	if got := Normalize(Input{Title: "Backend Engineer", Location: "Pune"}).RoleFamily; got != RoleSoftware {
		t.Errorf("Normalize role = %q, want %q", got, RoleSoftware)
	}
	if got := Normalize(Input{Title: "Staff Engineer", Department: "Engineering"}).RoleFamily; got != RoleSoftware {
		t.Errorf("the department must reach the classifier, got %q", got)
	}
	if got := Normalize(Input{Title: "HR Business Partner"}).RoleFamily; got != "" {
		t.Errorf("non-technical job got role %q", got)
	}
}
