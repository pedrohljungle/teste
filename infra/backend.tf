# State in S3, one key per workspace.
#
# workspace_key_prefix is what keeps sandbox and prod apart: Terraform writes
# <prefix>/<workspace>/<key>, so the two environments never share a state file and a mistaken
# apply in one cannot corrupt the other. The default workspace is deliberately unused — every
# apply happens in sandbox or prod.
#
# The bucket and the lock table are NOT created here: a backend cannot depend on the state it
# stores. Create them once, out of band (see README.md), before the first init.
terraform {
  backend "s3" {
    bucket               = "estrategia-pedro-test-tfstate"
    key                  = "pedro-test.tfstate"
    workspace_key_prefix = "workspaces"
    region               = "us-east-1"
    encrypt              = true
    # Native S3 locking (Terraform >= 1.10), so there is no DynamoDB table to maintain.
    use_lockfile = true
  }
}
