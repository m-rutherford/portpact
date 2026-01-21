output "vpc_id" {
  value = aws_vpc.this.id
}

output "private_subnet_ids" {
  value = [for s in aws_subnet.private : s.id]
}

output "gateway_instance_id" {
  value = aws_instance.gateway.id
}

output "rds_endpoint" {
  value = aws_db_instance.postgres.address
}

output "rds_username" {
  value = var.db_username
}

output "region" {
  value = var.aws_region
}

output "broker_api_url" {
  description = "URL for the broker API (POST /session)"
  value       = "${aws_apigatewayv2_api.broker.api_endpoint}/session"
}

output "broker_lambda_name" {
  value = aws_lambda_function.broker.function_name
}
