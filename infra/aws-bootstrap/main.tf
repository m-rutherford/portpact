provider "aws" {
  region = var.aws_region
}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)
  tags = {
    Project = var.name_prefix
  }
}

# -----------------------
# VPC (dedicated)
# -----------------------
resource "aws_vpc" "this" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(local.tags, {
    Name = "${var.name_prefix}-vpc"
  })
}

# Private subnets (2 AZs)
resource "aws_subnet" "private" {
  count                   = 2
  vpc_id                  = aws_vpc.this.id
  cidr_block              = var.private_subnet_cidrs[count.index]
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = false

  tags = merge(local.tags, {
    Name = "${var.name_prefix}-private-${local.azs[count.index]}"
    Tier = "private"
  })
}

# Route table: private only (no IGW, no NAT)
resource "aws_route_table" "private" {
  vpc_id = aws_vpc.this.id

  tags = merge(local.tags, {
    Name = "${var.name_prefix}-private-rt"
  })
}

resource "aws_route_table_association" "private" {
  count          = 2
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

# -----------------------
# Security Groups
# -----------------------
# Gateway SG: no inbound; outbound only to endpoints + DB
resource "aws_security_group" "gateway" {
  name        = "${var.name_prefix}-gateway-sg"
  description = "Portpact SSM gateway SG"
  vpc_id      = aws_vpc.this.id

  tags = merge(local.tags, { Name = "${var.name_prefix}-gateway-sg" })

  lifecycle {
    create_before_destroy = true
  }
}

# Endpoints SG: allow 443 inbound from gateway SG
resource "aws_security_group" "endpoints" {
  name        = "${var.name_prefix}-endpoints-sg"
  description = "Interface endpoints SG (SSM)"
  vpc_id      = aws_vpc.this.id

  ingress {
    description     = "HTTPS from gateway"
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    security_groups = [aws_security_group.gateway.id]
  }

  egress {
    description = "All egress"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(local.tags, { Name = "${var.name_prefix}-endpoints-sg" })

  lifecycle {
    create_before_destroy = true
  }
}

# Allow gateway outbound 443 ONLY to endpoints SG (tight)
resource "aws_security_group_rule" "gateway_to_endpoints_443" {
  type                     = "egress"
  security_group_id        = aws_security_group.gateway.id
  from_port                = 443
  to_port                  = 443
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.endpoints.id
  description              = "Gateway to SSM endpoints 443"
}

# -----------------------
# VPC Interface Endpoints (SSM)
# -----------------------
resource "aws_vpc_endpoint" "ssm" {
  vpc_id              = aws_vpc.this.id
  service_name        = "com.amazonaws.${var.aws_region}.ssm"
  vpc_endpoint_type   = "Interface"
  subnet_ids          = [for s in aws_subnet.private : s.id]
  security_group_ids  = [aws_security_group.endpoints.id]
  private_dns_enabled = true

  tags = merge(local.tags, { Name = "${var.name_prefix}-ssm" })
}

resource "aws_vpc_endpoint" "ssmmessages" {
  vpc_id              = aws_vpc.this.id
  service_name        = "com.amazonaws.${var.aws_region}.ssmmessages"
  vpc_endpoint_type   = "Interface"
  subnet_ids          = [for s in aws_subnet.private : s.id]
  security_group_ids  = [aws_security_group.endpoints.id]
  private_dns_enabled = true

  tags = merge(local.tags, { Name = "${var.name_prefix}-ssmmessages" })
}

resource "aws_vpc_endpoint" "ec2messages" {
  vpc_id              = aws_vpc.this.id
  service_name        = "com.amazonaws.${var.aws_region}.ec2messages"
  vpc_endpoint_type   = "Interface"
  subnet_ids          = [for s in aws_subnet.private : s.id]
  security_group_ids  = [aws_security_group.endpoints.id]
  private_dns_enabled = true

  tags = merge(local.tags, { Name = "${var.name_prefix}-ec2messages" })
}

# -----------------------
# IAM for gateway (SSM managed)
# -----------------------
data "aws_iam_policy" "ssm_core" {
  arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role" "gateway" {
  name = "${var.name_prefix}-gateway-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect = "Allow",
      Principal = { Service = "ec2.amazonaws.com" },
      Action = "sts:AssumeRole"
    }]
  })
  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "gateway_ssm" {
  role       = aws_iam_role.gateway.name
  policy_arn = data.aws_iam_policy.ssm_core.arn
}

resource "aws_iam_instance_profile" "gateway" {
  name = "${var.name_prefix}-gateway-profile"
  role = aws_iam_role.gateway.name
}

# -----------------------
# Gateway EC2 (no public IP)
# -----------------------
data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }
}

resource "aws_instance" "gateway" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.gateway_instance_type
  subnet_id              = aws_subnet.private[0].id
  vpc_security_group_ids = [aws_security_group.gateway.id]
  iam_instance_profile   = aws_iam_instance_profile.gateway.name

  tags = merge(local.tags, {
    Name               = "${var.name_prefix}-gateway"
    "portpact:gateway" = "true"
  })

  depends_on = [
    aws_vpc_endpoint.ssm,
    aws_vpc_endpoint.ssmmessages,
    aws_vpc_endpoint.ec2messages
  ]
}

# -----------------------
# RDS Postgres (private)
# -----------------------
resource "aws_db_subnet_group" "db" {
  name       = "${var.name_prefix}-db-subnets"
  subnet_ids = [for s in aws_subnet.private : s.id]

  tags = merge(local.tags, { Name = "${var.name_prefix}-db-subnets" })
}

resource "aws_security_group" "rds" {
  name        = "${var.name_prefix}-rds-sg"
  description = "Portpact RDS Postgres SG"
  vpc_id      = aws_vpc.this.id

  ingress {
    description     = "Postgres from gateway"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.gateway.id]
  }

  egress {
    description = "All egress"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(local.tags, { Name = "${var.name_prefix}-rds-sg" })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_security_group_rule" "gateway_to_rds_5432" {
  type                     = "egress"
  security_group_id        = aws_security_group.gateway.id
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.rds.id
  description              = "Gateway to RDS Postgres 5432"
}

resource "aws_db_instance" "postgres" {
  identifier             = "${var.name_prefix}-postgres"
  engine                 = "postgres"
  engine_version         = "16"
  instance_class         = var.db_instance_class

  allocated_storage      = 20
  storage_encrypted      = true

  db_name                = var.db_name
  username               = var.db_username
  password               = var.db_password

  db_subnet_group_name   = aws_db_subnet_group.db.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  publicly_accessible    = false

  skip_final_snapshot    = true
  deletion_protection    = false

  tags = merge(local.tags, { Name = "${var.name_prefix}-postgres" })
}

# -----------------------
# Broker Lambda + API Gateway
# -----------------------

# IAM role for Lambda
resource "aws_iam_role" "broker" {
  name = "${var.name_prefix}-broker-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action = "sts:AssumeRole"
    }]
  })
  tags = local.tags
}

# CloudWatch Logs permissions
resource "aws_iam_role_policy_attachment" "broker_logs" {
  role       = aws_iam_role.broker.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

# SSM StartSession permission for the gateway instance
resource "aws_iam_role_policy" "broker_ssm" {
  name = "${var.name_prefix}-broker-ssm"
  role = aws_iam_role.broker.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = "ssm:StartSession"
        Resource = [
          aws_instance.gateway.arn,
          "arn:aws:ssm:${var.aws_region}::document/AWS-StartPortForwardingSessionToRemoteHost"
        ]
      }
    ]
  })
}

# Lambda function
resource "aws_lambda_function" "broker" {
  function_name = "${var.name_prefix}-broker"
  role          = aws_iam_role.broker.arn
  handler       = "index.handler"
  runtime       = "nodejs20.x"
  timeout       = 30
  memory_size   = 256

  filename         = "${path.module}/../broker/broker.zip"
  source_code_hash = filebase64sha256("${path.module}/../broker/broker.zip")

  environment {
    variables = {
      BROKER_API_KEY      = var.broker_api_key
      GATEWAY_INSTANCE_ID = aws_instance.gateway.id
      ALLOWED_RDS_HOST    = aws_db_instance.postgres.address
      ALLOWED_RDS_PORT    = "5432"
    }
  }

  tags = local.tags
}

# API Gateway HTTP API
resource "aws_apigatewayv2_api" "broker" {
  name          = "${var.name_prefix}-broker-api"
  protocol_type = "HTTP"

  tags = local.tags
}

# Lambda integration
resource "aws_apigatewayv2_integration" "broker" {
  api_id             = aws_apigatewayv2_api.broker.id
  integration_type   = "AWS_PROXY"
  integration_method = "POST"
  integration_uri    = aws_lambda_function.broker.invoke_arn
}

# Route: POST /session
resource "aws_apigatewayv2_route" "broker_session" {
  api_id    = aws_apigatewayv2_api.broker.id
  route_key = "POST /session"
  target    = "integrations/${aws_apigatewayv2_integration.broker.id}"
}

# Default stage with auto-deploy
resource "aws_apigatewayv2_stage" "broker" {
  api_id      = aws_apigatewayv2_api.broker.id
  name        = "$default"
  auto_deploy = true

  tags = local.tags
}

# Allow API Gateway to invoke Lambda
resource "aws_lambda_permission" "broker_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.broker.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.broker.execution_arn}/*/*"
}
