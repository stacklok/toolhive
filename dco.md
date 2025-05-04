# Developer Certificate of Origin (DCO)
In order to contribute to the project, you must agree to the Developer Certificate of Origin. A [Developer Certificate of Origin (DCO)](https://developercertificate.org/)
is an affirmation that the developer contributing the proposed changes has the necessary rights to submit those changes.
A DCO provides some additional legal protections while being relatively easy to do. 

The entire DCO can be summarized as:
- Certify that the submitted code can be submitted under the open source license of the project (e.g. Apache 2.0)
- I understand that what I am contributing is public and will be redistributed indefinitely


## How to Use Developer Certificate of Origin
In order to contribute to the project, you must agree to the Developer Certificate of Origin. To confirm that you agree, your commit message must include a Signed-off-by trailer at the bottom of the commit message. 

For example, it might look like the following:
```bash
A commit message

Closes gh-345

Signed-off-by: jane marmot <jmarmot@example.org>
```

The Signed-off-by [trailer](https://git-scm.com/docs/git-interpret-trailers) can be added automatically by using the [-s or –signoff command line option](https://git-scm.com/docs/git-commit/2.13.7#Documentation/git-commit.txt--s) when specifying your commit message:
```bash
git commit -s -m
```
If you have chosen the [Keep my email address private](https://docs.github.com/en/account-and-profile/setting-up-and-managing-your-personal-account-on-github/managing-email-preferences/setting-your-commit-email-address#about-commit-email-addresses) option within GitHub, the Signed-off-by trailer might look something like:
```bash
A commit message

Closes gh-345

Signed-off-by: jane marmot <462403+jmarmot@users.noreply.github.com>
```
