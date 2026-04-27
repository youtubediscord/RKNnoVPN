package com.rknnovpn.panel.ui.navigation

import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import com.rknnovpn.panel.ui.apps.AppPickerScreen
import com.rknnovpn.panel.ui.dashboard.DashboardScreen
import com.rknnovpn.panel.ui.nodes.NodeListScreen
import com.rknnovpn.panel.ui.audit.AuditScreen
import com.rknnovpn.panel.ui.settings.SettingsScreen

const val AUDIT_ROUTE = "audit"

@Composable
fun NavGraph(
    navController: NavHostController,
    modifier: Modifier = Modifier,
) {
    NavHost(
        navController = navController,
        startDestination = TopLevelRoute.Dashboard.route,
        modifier = modifier,
    ) {
        composable(TopLevelRoute.Dashboard.route) {
            DashboardScreen()
        }
        composable(TopLevelRoute.Nodes.route) {
            NodeListScreen()
        }
        composable(TopLevelRoute.Apps.route) {
            AppPickerScreen()
        }
        composable(TopLevelRoute.Settings.route) {
            SettingsScreen(
                onNavigateToAudit = { navController.navigate(AUDIT_ROUTE) },
            )
        }
        composable(AUDIT_ROUTE) {
            AuditScreen()
        }
    }
}
