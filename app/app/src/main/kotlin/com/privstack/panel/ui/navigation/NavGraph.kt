package com.privstack.panel.ui.navigation

import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import com.privstack.panel.ui.apps.AppPickerScreen
import com.privstack.panel.ui.dashboard.DashboardScreen
import com.privstack.panel.ui.nodes.NodeListScreen
import com.privstack.panel.ui.audit.AuditScreen
import com.privstack.panel.ui.settings.SettingsScreen

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
