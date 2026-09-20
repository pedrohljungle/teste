# The index in locals.tf already fails when the workspace is not an environment, but with
# "invalid index", which says nothing about what to do. This precondition fails first, with a
# sentence — and it BLOCKS the plan, which a check block would not: check emits warnings.
resource "terraform_data" "workspace_guard" {
  input = local.environment

  lifecycle {
    precondition {
      condition     = contains(["sandbox", "prod"], terraform.workspace)
      error_message = "Workspace '${terraform.workspace}' is not an environment. Run: terraform workspace select sandbox|prod"
    }
  }
}
