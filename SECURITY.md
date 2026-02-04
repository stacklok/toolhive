# Security Policy

The ToolHive community take security seriously! We appreciate your efforts to
disclose your findings responsibly and will make every effort to acknowledge
your contributions.

## Reporting a vulnerability

To report a security issue, please use the GitHub Security Advisory
["Report a Vulnerability"](https://github.com/stacklok/toolhive/security/advisories/new)
tab.

If you are unable to access GitHub you can also email us at
[security@stacklok.com](mailto:security@stacklok.com).

Include steps to reproduce the vulnerability, the vulnerable versions, and any
additional files to reproduce the vulnerability.

If you are only comfortable sharing under GPG, please start by sending an email
requesting a public PGP key to use for encryption.

### Contacting the ToolHive security team

Contact the team by sending email to
[security@stacklok.com](mailto:security@stacklok.com).

## Disclosures

### Private disclosure processes

The ToolHive community asks that all suspected vulnerabilities be handled in
accordance with
[Responsible Disclosure model](https://en.wikipedia.org/wiki/Responsible_disclosure).

### Public disclosure processes

If anyone knows of a publicly disclosed security vulnerability please
IMMEDIATELY email [security@stacklok.com](mailto:security@stacklok.com) to
inform us about the vulnerability so that we may start the patch, release, and
communication process.

If a reporter contacts the us to express intent to make an issue public before a
fix is available, we will request if the issue can be handled via a private
disclosure process. If the reporter denies the request, we will move swiftly
with the fix and release process.

## Patch, release, and public communication

For each vulnerability, the ToolHive security team will coordinate to create the
fix and release, and notify the rest of the community.

All of the timelines below are suggestions and assume a Private Disclosure.

- The security team drives the schedule using their best judgment based on
  severity, development time, and release work.
- If the security team is dealing with a Public Disclosure all timelines become
  ASAP.
- If the fix relies on another upstream project's disclosure timeline, that will
  adjust the process as well.
- We will work with the upstream project to fit their timeline and best protect
  ToolHive users.
- The Security team will give advance notice to the Private Distributors list
  before the fix is released.

### Fix team organization

These steps should be completed within the first 24 hours of Disclosure.

- The security team will work quickly to identify relevant engineers from the
  affected projects and packages and being those engineers into the
  [security advisory](https://docs.github.com/en/code-security/security-advisories/)
  thread.
- These selected developers become the "Fix Team" (the fix team is often drawn
  from the projects MAINTAINERS)

### Fix development process

These steps should be completed within the 1-7 days of Disclosure.

- Create a new
  [security advisory](https://docs.github.com/en/code-security/security-advisories/)
  in affected repository by visiting
  `https://github.com/stacklok/toolhive/security/advisories/new`
- As many details as possible should be entered such as versions affected, CVE
  (if available yet). As more information is discovered, edit and update the
  advisory accordingly.
- Use the CVSS calculator to score a severity level.
  ![CVSS Calculator](/images/calc.png)
- Add collaborators from codeowners team only (outside members can only be added
  after approval from the security team)
- The reporter may be added to the issue to assist with review, but **only
  reporters who have contacted the security team using a private channel**.
- Select 'Request CVE' ![Request CVE](/docs/static/img/cve.png)
- The security team / Fix Team create a private temporary fork
  ![Security Fork](/docs/static/img/fork.png)
- The Fix team performs all work in a 'security advisory' within its temporary
  fork
- CI can be checked locally using the [act](https://github.com/nektos/act)
  project
- All communication happens within the security advisory, it is _not_ discussed
  in slack channels or non private issues.
- The Fix Team will notify the security team that work on the fix branch is
  completed, this can be done by tagging names in the advisory
- The Fix team and the security team will agree on fix release day
- The recommended release time is 4pm UTC on a non-Friday weekday. This means
  the announcement will be seen morning Pacific, early evening Europe, and late
  evening Asia.

If the CVSS score is under ~4.0
([a low severity score](https://www.first.org/cvss/specification-document#i5))
or the assessed risk is low the Fix Team can decide to slow the release process
down in the face of holidays, developer bandwidth, etc.

Note: CVSS is convenient but imperfect. Ultimately, the security team has
discretion on classifying the severity of a vulnerability.

The severity of the bug and related handling decisions must be discussed on in
the security advisory, never in public repos.

### Fix disclosure process

With the Fix Development underway, the security team needs to come up with an
overall communication plan for the wider community. This Disclosure process
should begin after the Fix Team has developed a Fix or mitigation so that a
realistic timeline can be communicated to users.

**Fix release day** (Completed within 1-21 days of Disclosure)

- The Fix Team will approve the related pull requests in the private temporary
  branch of the security advisory
- The security team will merge the security advisory / temporary fork and its
  commits into the main branch of the affected repository
  ![Security Advisory](docs/images/publish.png)
- The security team will ensure all the binaries are built, signed, publicly
  available, and functional.
- The security team will announce the new releases, the CVE number, severity,
  and impact, and the location of the binaries to get wide distribution and user
  action. As much as possible this announcement should be actionable, and
  include any mitigating steps users can take prior to upgrading to a fixed
  version. An announcement template is available below. The announcement will be
  sent to the following channels:
- A link to fix will be posted to the
  [Stacklok Discord Server](https://discord.gg/stacklok) in the #toolhive
  channel.

## Retrospective

These steps should be completed 1-3 days after the Release Date. The
retrospective process
[should be blameless](https://landing.google.com/sre/book/chapters/postmortem-culture.html).

- The security team will send a retrospective of the process to the
  [Stacklok Discord Server](https://discord.gg/stacklok) including details on
  everyone involved, the timeline of the process, links to relevant PRs that
  introduced the issue, if relevant, and any critiques of the response and
  release process.
