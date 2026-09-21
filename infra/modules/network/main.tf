# The network is the whole security story of this stack: what can reach the internet, and what
# the internet can reach. Everything else (security groups) only narrows it further.
#
#   public subnets  -> have a route to the internet gateway. Only the ALB lives here.
#   private subnets -> route outbound through NAT, and have no inbound route from the internet.
#                      ECS tasks and RDS live here.

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  # Two AZs, which is the minimum an ALB requires and the minimum for an RDS failover to have
  # somewhere to go.
  azs = slice(data.aws_availability_zones.available.names, 0, 2)

  # /20 blocks out of the VPC CIDR: 0 and 1 public, 2 and 3 private.
  public_cidrs  = [for index in range(2) : cidrsubnet(var.vpc_cidr, 4, index)]
  private_cidrs = [for index in range(2) : cidrsubnet(var.vpc_cidr, 4, index + 2)]

  nat_count = var.single_nat_gateway ? 1 : length(local.azs)
}

resource "aws_vpc" "this" {
  cidr_block = var.vpc_cidr
  # Both are required for RDS to be reachable by name from the tasks.
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = var.name }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = var.name }
}

resource "aws_subnet" "public" {
  count = length(local.azs)

  vpc_id            = aws_vpc.this.id
  cidr_block        = local.public_cidrs[count.index]
  availability_zone = local.azs[count.index]
  # Only the ALB is placed here, and it brings its own addresses; nothing else should get a
  # public IP by accident.
  map_public_ip_on_launch = false

  tags = { Name = "${var.name}-public-${local.azs[count.index]}" }
}

resource "aws_subnet" "private" {
  count = length(local.azs)

  vpc_id            = aws_vpc.this.id
  cidr_block        = local.private_cidrs[count.index]
  availability_zone = local.azs[count.index]

  tags = { Name = "${var.name}-private-${local.azs[count.index]}" }
}

resource "aws_eip" "nat" {
  count  = local.nat_count
  domain = "vpc"
  tags   = { Name = "${var.name}-nat-${count.index}" }
}

# NAT lives in a PUBLIC subnet and serves the private ones: that is what gives the tasks
# outbound access (ECR, SQS, the IDP) without giving anything inbound access to them.
resource "aws_nat_gateway" "this" {
  count = local.nat_count

  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.public[count.index].id

  tags       = { Name = "${var.name}-${count.index}" }
  depends_on = [aws_internet_gateway.this]
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "${var.name}-public" }
}

resource "aws_route" "public_internet" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.this.id
}

resource "aws_route_table_association" "public" {
  count = length(aws_subnet.public)

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# One route table per private subnet, so each AZ can point at its own NAT when there is more
# than one. With a single NAT they all point at the same gateway.
resource "aws_route_table" "private" {
  count = length(local.azs)

  vpc_id = aws_vpc.this.id
  tags   = { Name = "${var.name}-private-${local.azs[count.index]}" }
}

resource "aws_route" "private_nat" {
  count = length(local.azs)

  route_table_id         = aws_route_table.private[count.index].id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.this[var.single_nat_gateway ? 0 : count.index].id
}

resource "aws_route_table_association" "private" {
  count = length(aws_subnet.private)

  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[count.index].id
}
